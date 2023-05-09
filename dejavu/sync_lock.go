// DejaVu - Data snapshot and sync.
// Copyright (c) 2022-present, b3log.org
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package dejavu

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/88250/gulu"
	"github.com/wangxu0213/esnote-kernel/dejavu/cloud"
	"github.com/wangxu0213/esnote-kernel/eventbus"
	"github.com/wangxu0213/esnote-kernel/logging"
)

var (
	ErrLockCloudFailed = errors.New("lock cloud repo failed")
	ErrCloudLocked     = errors.New("cloud repo is locked")
)

func (repo *Repo) unlockCloud(context map[string]interface{}) {
	endRefreshLock <- true
	var err error
	for i := 0; i < 3; i++ {
		eventbus.Publish(eventbus.EvtCloudUnlock, context)
		err = repo.cloud.RemoveObject("lock-sync")
		if nil == err {
			return
		}
	}

	if errors.Is(err, cloud.ErrCloudAuthFailed) {
		return
	}

	logging.LogErrorf("unlock cloud repo failed: %s", err)
	return
}

var endRefreshLock = make(chan bool)

func (repo *Repo) tryLockCloud(context map[string]interface{}) (err error) {
	for i := 0; i < 3; i++ {
		eventbus.Publish(eventbus.EvtCloudLock, context)
		err = repo.lockCloud()
		if nil != err {
			if errors.Is(err, ErrCloudLocked) {
				logging.LogInfof("cloud repo is locked, retry after 5s")
				time.Sleep(5 * time.Second)
				continue
			}
			return
		}

		// 锁定成功，定时刷新所
		go func() {
			for {
				select {
				case <-endRefreshLock:
					return
				case <-time.After(30 * time.Second):
					if refershErr := repo.lockCloud0(); nil != refershErr {
						logging.LogErrorf("refresh cloud repo lock failed: %s", refershErr)
					}
				}
			}
		}()

		return
	}
	return
}

func (repo *Repo) lockCloud() (err error) {
	data, err := repo.cloud.DownloadObject("lock-sync")
	if errors.Is(err, cloud.ErrCloudObjectNotFound) {
		err = repo.lockCloud0()
		return
	}

	content := map[string]interface{}{}
	err = gulu.JSON.UnmarshalJSON(data, &content)
	if nil != err {
		logging.LogErrorf("unmarshal lock sync failed: %s", err)
		return
	}

	deviceID := content["deviceID"].(string)
	t := int64(content["time"].(float64))
	now := time.Now()
	lockTime := time.UnixMilli(t)
	if now.After(lockTime.Add(65*time.Second)) || deviceID == repo.DeviceID {
		// 云端锁超时过期或者就是当前设备锁的，那么当前设备可以继续直接锁
		err = repo.lockCloud0()
		return
	}

	logging.LogWarnf("cloud repo is locked by device [%s] at [%s], will retry after 30s", content["deviceID"].(string), lockTime.Format("2006-01-02 15:04:05"))
	err = ErrCloudLocked
	return
}

func (repo *Repo) lockCloud0() (err error) {
	lockSync := filepath.Join(repo.Path, "lock-sync")
	content := map[string]interface{}{
		"deviceID": repo.DeviceID,
		"time":     time.Now().UnixMilli(),
	}
	data, err := gulu.JSON.MarshalJSON(content)
	if nil != err {
		logging.LogErrorf("marshal lock sync failed: %s", err)
		err = ErrLockCloudFailed
		return
	}
	err = gulu.File.WriteFileSafer(lockSync, data, 0644)
	if nil != err {
		logging.LogErrorf("write lock sync failed: %s", err)
		err = ErrCloudLocked
		return
	}

	err = repo.cloud.UploadObject("lock-sync", true)
	if nil != err {
		if errors.Is(err, cloud.ErrSystemTimeIncorrect) || errors.Is(err, cloud.ErrCloudAuthFailed) {
			return
		}

		logging.LogErrorf("upload lock sync failed: %s", err)
		err = ErrLockCloudFailed
	}
	return
}

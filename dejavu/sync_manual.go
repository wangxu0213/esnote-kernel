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
	"fmt"
	"path/filepath"
	"time"

	"github.com/88250/gulu"
	"github.com/wangxu0213/esnote-kernel/dejavu/cloud"
	"github.com/wangxu0213/esnote-kernel/dejavu/entity"
	"github.com/wangxu0213/esnote-kernel/logging"
)

func (repo *Repo) SyncDownload(context map[string]interface{}) (mergeResult *MergeResult, trafficStat *TrafficStat, err error) {
	lock.Lock()
	defer lock.Unlock()

	// 锁定云端，防止其他设备并发上传数据
	err = repo.tryLockCloud(context)
	if nil != err {
		return
	}
	defer repo.unlockCloud(context)

	mergeResult = &MergeResult{Time: time.Now()}
	trafficStat = &TrafficStat{}

	// 获取本地最新索引
	latest, err := repo.Latest()
	if nil != err {
		logging.LogErrorf("get latest failed: %s", err)
		return
	}

	// 从云端获取最新索引
	length, cloudLatest, err := repo.downloadCloudLatest(context)
	if nil != err {
		if !errors.Is(err, cloud.ErrCloudObjectNotFound) {
			logging.LogErrorf("download cloud latest failed: %s", err)
			return
		}
	}
	trafficStat.DownloadFileCount++
	trafficStat.DownloadBytes += length
	trafficStat.APIGet++

	if cloudLatest.ID == latest.ID || "" == cloudLatest.ID {
		// 数据一致或者云端为空，直接返回
		return
	}

	// 计算本地缺失的文件
	fetchFileIDs, err := repo.localNotFoundFiles(cloudLatest.Files)
	if nil != err {
		logging.LogErrorf("get local not found files failed: %s", err)
		return
	}

	// 从云端下载缺失文件并入库
	length, fetchedFiles, err := repo.downloadCloudFilesPut(fetchFileIDs, context)
	if nil != err {
		logging.LogErrorf("download cloud files put failed: %s", err)
		return
	}
	trafficStat.DownloadFileCount += len(fetchFileIDs)
	trafficStat.DownloadBytes += length
	trafficStat.APIGet += trafficStat.DownloadFileCount

	// 组装还原云端最新文件列表
	cloudLatestFiles, err := repo.getFiles(cloudLatest.Files)
	if nil != err {
		logging.LogErrorf("get cloud latest files failed: %s", err)
		return
	}

	// 从文件列表中得到去重后的分块列表
	cloudChunkIDs := repo.getChunks(cloudLatestFiles)

	// 计算本地缺失的分块
	fetchChunkIDs, err := repo.localNotFoundChunks(cloudChunkIDs)
	if nil != err {
		logging.LogErrorf("get local not found chunks failed: %s", err)
		return
	}

	// 从云端下载缺失分块并入库
	length, err = repo.downloadCloudChunksPut(fetchChunkIDs, context)
	trafficStat.DownloadBytes += length
	trafficStat.DownloadChunkCount += len(fetchChunkIDs)
	trafficStat.APIGet += trafficStat.DownloadChunkCount

	// 计算本地相比上一个同步点的 upsert 和 remove 差异
	latestFiles, err := repo.getFiles(latest.Files)
	if nil != err {
		logging.LogErrorf("get latest files failed: %s", err)
		return
	}
	latestSync := repo.latestSync()
	latestSyncFiles, err := repo.getFiles(latestSync.Files)
	if nil != err {
		logging.LogErrorf("get latest sync files failed: %s", err)
		return
	}
	localUpserts, _ := repo.DiffUpsertRemove(latestFiles, latestSyncFiles)

	// 计算云端最新相比本地最新的 upsert 和 remove 差异
	// 在单向同步的情况下该结果可直接作为合并结果
	mergeResult.Upserts, mergeResult.Removes = repo.DiffUpsertRemove(cloudLatestFiles, latestFiles)

	var fetchedFileIDs []string
	for _, fetchedFile := range fetchedFiles {
		fetchedFileIDs = append(fetchedFileIDs, fetchedFile.ID)
	}

	// 计算冲突的 upsert
	// 冲突的文件以云端 upsert 和 remove 为准
	for _, localUpsert := range localUpserts {
		if repo.existDataFile(mergeResult.Upserts, localUpsert) || repo.existDataFile(mergeResult.Removes, localUpsert) {
			mergeResult.Conflicts = append(mergeResult.Conflicts, localUpsert)
		}
	}

	// 冲突文件复制到数据历史文件夹
	if 0 < len(mergeResult.Conflicts) {
		now := mergeResult.Time.Format("2006-01-02-150405")
		temp := filepath.Join(repo.TempPath, "repo", "sync", "conflicts", now)
		for _, file := range mergeResult.Conflicts {
			var checkoutTmp *entity.File
			checkoutTmp, err = repo.store.GetFile(file.ID)
			if nil != err {
				logging.LogErrorf("get file failed: %s", err)
				return
			}

			err = repo.checkoutFile(checkoutTmp, temp, context)
			if nil != err {
				logging.LogErrorf("checkout file failed: %s", err)
				return
			}

			absPath := filepath.Join(temp, checkoutTmp.Path)
			err = repo.genSyncHistory(now, file.Path, absPath)
			if nil != err {
				logging.LogErrorf("generate sync history failed: %s", err)
				err = ErrCloudGenerateConflictHistory
				return
			}
		}
	}

	// 数据变更后需要还原工作区并创建 merge 快照
	if 0 < len(mergeResult.Upserts) || 0 < len(mergeResult.Removes) {
		if 0 < len(mergeResult.Upserts) {
			// 迁出到工作区
			err = repo.checkoutFiles(mergeResult.Upserts, context)
			if nil != err {
				logging.LogErrorf("checkout files failed: %s", err)
				return
			}
		}

		if 0 < len(mergeResult.Removes) {
			// 删除工作区文件
			err = repo.removeFiles(mergeResult.Removes, context)
			if nil != err {
				logging.LogErrorf("remove files failed: %s", err)
				return
			}
		}
	}

	// 创建 merge 快照
	mergeStart := time.Now()
	latest, err = repo.index("[Sync] Cloud sync merge", context)
	if nil != err {
		logging.LogErrorf("merge index failed: %s", err)
		return
	}
	mergeElapsed := time.Since(mergeStart)
	mergeMemo := fmt.Sprintf("[Sync] Cloud sync merge, completed in %.2fs", mergeElapsed.Seconds())
	latest.Memo = mergeMemo
	err = repo.store.PutIndex(latest)
	if nil != err {
		logging.LogErrorf("put merge index failed: %s", err)
		return
	}

	// 更新本地 latest
	err = repo.UpdateLatest(latest.ID)
	if nil != err {
		logging.LogErrorf("update latest failed: %s", err)
		return
	}

	// 更新本地同步点
	err = repo.UpdateLatestSync(latest.ID)
	if nil != err {
		logging.LogErrorf("update latest sync failed: %s", err)
		return
	}

	// 统计流量
	go repo.cloud.AddTraffic(&cloud.Traffic{
		DownloadBytes: trafficStat.DownloadBytes,
		APIGet:        trafficStat.APIGet,
	})

	// 移除空目录
	err = gulu.File.RemoveEmptyDirs(repo.DataPath, removeEmptyDirExcludes...)
	if nil != err {
		logging.LogErrorf("remove empty dirs failed: %s", err)
		return
	}
	return
}

func (repo *Repo) SyncUpload(context map[string]interface{}) (trafficStat *TrafficStat, err error) {
	lock.Lock()
	defer lock.Unlock()

	// 锁定云端，防止其他设备并发上传数据
	err = repo.tryLockCloud(context)
	if nil != err {
		return
	}
	defer repo.unlockCloud(context)

	trafficStat = &TrafficStat{}

	latest, err := repo.Latest()
	if nil != err {
		logging.LogErrorf("get latest failed: %s", err)
		return
	}

	// 从云端获取最新索引
	length, cloudLatest, err := repo.downloadCloudLatest(context)
	if nil != err {
		if !errors.Is(err, cloud.ErrCloudObjectNotFound) {
			logging.LogErrorf("download cloud latest failed: %s", err)
			return
		}
	}
	trafficStat.DownloadFileCount++
	trafficStat.DownloadBytes += length
	trafficStat.APIPut++

	if cloudLatest.ID == latest.ID {
		// 数据一致，直接返回
		return
	}

	availableSize := repo.cloud.GetAvailableSize()
	if availableSize <= cloudLatest.Size || availableSize <= latest.Size {
		err = ErrCloudStorageSizeExceeded
		return
	}

	// 计算云端缺失的文件
	var uploadFiles []*entity.File
	for _, localFileID := range latest.Files {
		if !gulu.Str.Contains(localFileID, cloudLatest.Files) {
			var uploadFile *entity.File
			uploadFile, err = repo.store.GetFile(localFileID)
			if nil != err {
				logging.LogErrorf("get file failed: %s", err)
				return
			}
			uploadFiles = append(uploadFiles, uploadFile)
		}
	}

	// 从文件列表中得到去重后的分块列表
	uploadChunkIDs := repo.getChunks(uploadFiles)

	// 这里暂时不计算云端缺失的分块了，因为目前计数云端缺失分块的代价太大
	//uploadChunkIDs, err = repo.cloud.GetChunks(uploadChunkIDs)
	//if nil != err {
	//	logging.LogErrorf("get cloud repo upload chunks failed: %s", err)
	//	return
	//}

	// 上传分块
	length, err = repo.uploadChunks(uploadChunkIDs, context)
	if nil != err {
		logging.LogErrorf("upload chunks failed: %s", err)
		return
	}
	trafficStat.UploadChunkCount += len(uploadChunkIDs)
	trafficStat.UploadBytes += length
	trafficStat.APIPut += trafficStat.UploadChunkCount

	// 上传文件
	length, err = repo.uploadFiles(uploadFiles, context)
	if nil != err {
		logging.LogErrorf("upload files failed: %s", err)
		return
	}
	trafficStat.UploadChunkCount += len(uploadFiles)
	trafficStat.UploadBytes += length
	trafficStat.APIPut += trafficStat.UploadChunkCount

	// 上传索引
	length, err = repo.uploadIndex(latest, context)
	if nil != err {
		logging.LogErrorf("upload indexes failed: %s", err)
		return
	}
	trafficStat.UploadFileCount++
	trafficStat.UploadBytes += length
	trafficStat.APIPut++

	// 更新云端 latest
	length, err = repo.updateCloudRef("refs/latest", context)
	if nil != err {
		logging.LogErrorf("update cloud [refs/latest] failed: %s", err)
		return
	}
	trafficStat.UploadFileCount++
	trafficStat.UploadBytes += length
	trafficStat.APIPut++

	// 更新云端索引列表
	downloadBytes, uploadBytes, err := repo.updateCloudIndexes(latest, context)
	if nil != err {
		logging.LogErrorf("update cloud indexes failed: %s", err)
		return
	}
	trafficStat.DownloadBytes += downloadBytes
	trafficStat.UploadBytes += uploadBytes
	trafficStat.APIGet++
	trafficStat.APIPut++

	// 统计流量
	go repo.cloud.AddTraffic(&cloud.Traffic{
		UploadBytes: trafficStat.UploadBytes,
		APIPut:      trafficStat.APIPut,
	})
	return
}

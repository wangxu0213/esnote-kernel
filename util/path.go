// SiYuan - Build Your Eternal Digital Garden
// Copyright (c) 2020-present, b3log.org
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

package util

import (
	"bytes"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/88250/gulu"
	"github.com/wangxu0213/esnote-logging"
)

var (
	SSL       = false
	UserAgent = "SiYuan/" + Ver
)

const (
	AliyunServer     = "https://siyuan-sync.b3logfile.com"  // 云端服务地址，阿里云负载均衡，用于接口，数据同步文件上传、下载会走七牛云 OSS SiYuanSyncServer
	SiYuanSyncServer = "https://siyuan-data.b3logfile.com/" // 云端数据同步服务地址，七牛云 OSS，用于数据同步文件上传、下载
	BazaarStatServer = "http://bazaar.b3logfile.com"        // 集市包统计服务地址，直接对接 Bucket 没有 CDN 缓存
	BazaarOSSServer  = "https://oss.b3logfile.com"          // 云端对象存储地址，七牛云，仅用于读取集市包
	LiandiServer     = "https://ld246.com"                  // 链滴服务地址，用于分享发布帖子
)

func ShortPathForBootingDisplay(p string) string {
	if 25 > len(p) {
		return p
	}
	p = strings.TrimSuffix(p, ".sy")
	p = path.Base(p)
	return p
}

var LocalIPs []string

func GetLocalIPs() (ret []string) {
	if ContainerAndroid == Container {
		// Android 上用不了 net.InterfaceAddrs() https://github.com/golang/go/issues/40569，所以前面使用启动内核传入的参数 localIPs
		LocalIPs = append(LocalIPs, LocalHost)
		LocalIPs = gulu.Str.RemoveDuplicatedElem(LocalIPs)
		return LocalIPs
	}

	ret = []string{}
	addrs, err := net.InterfaceAddrs()
	if nil != err {
		logging.LogWarnf("get interface addresses failed: %s", err)
		return
	}
	for _, addr := range addrs {
		if networkIp, ok := addr.(*net.IPNet); ok && !networkIp.IP.IsLoopback() && networkIp.IP.To4() != nil &&
			bytes.Equal([]byte{255, 255, 255, 0}, networkIp.Mask) {
			ret = append(ret, networkIp.IP.String())
		}
	}
	ret = append(ret, LocalHost)
	ret = gulu.Str.RemoveDuplicatedElem(ret)
	return
}

func isRunningInDockerContainer() bool {
	if _, runInContainer := os.LookupEnv("RUN_IN_CONTAINER"); runInContainer {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}

func IsRelativePath(dest string) bool {
	if 1 > len(dest) {
		return true
	}

	if '/' == dest[0] {
		return false
	}
	return !strings.Contains(dest, ":/") && !strings.Contains(dest, ":\\")
}

func TimeFromID(id string) (ret string) {
	if 14 > len(id) {
		logging.LogWarnf("invalid id [%s], stack [\n%s]", id, logging.ShortStack())
		return time.Now().Format("20060102150405")
	}
	ret = id[:14]
	return
}

func GetChildDocDepth(treeAbsPath string) (ret int) {
	dir := strings.TrimSuffix(treeAbsPath, ".sy")
	if !gulu.File.IsDir(dir) {
		return
	}

	baseDepth := strings.Count(filepath.ToSlash(treeAbsPath), "/")
	depth := 1
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		p := filepath.ToSlash(path)
		currentDepth := strings.Count(p, "/")
		if depth < currentDepth {
			depth = currentDepth
		}
		return nil
	})
	ret = depth - baseDepth
	return
}

func NormalizeTimeout(timeout int) int {
	if 7 > timeout {
		if 1 > timeout {
			return 30
		}
		return 7
	}
	if 300 < timeout {
		return 300
	}
	return timeout
}

func NormalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if "" == endpoint {
		return ""
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	if !strings.HasSuffix(endpoint, "/") {
		endpoint = endpoint + "/"
	}
	return endpoint
}

func FilterMoveDocFromPaths(fromPaths []string, toPath string) (ret []string) {
	tmp := FilterSelfChildDocs(fromPaths)
	for _, fromPath := range tmp {
		fromDir := strings.TrimSuffix(fromPath, ".sy")
		if strings.HasPrefix(toPath, fromDir) {
			continue
		}
		ret = append(ret, fromPath)
	}
	return
}

func FilterSelfChildDocs(paths []string) (ret []string) {
	sort.Slice(paths, func(i, j int) bool { return strings.Count(paths[i], "/") < strings.Count(paths[j], "/") })

	dirs := map[string]string{}
	for _, fromPath := range paths {
		dir := strings.TrimSuffix(fromPath, ".sy")
		existParent := false
		for d, _ := range dirs {
			if strings.HasPrefix(fromPath, d) {
				existParent = true
				break
			}
		}
		if existParent {
			continue
		}
		dirs[dir] = fromPath
		ret = append(ret, fromPath)
	}
	return
}

func IsAssetLinkDest(dest []byte) bool {
	return bytes.HasPrefix(dest, []byte("assets/"))
}

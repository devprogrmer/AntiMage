//go:build linux

package system

import (
	"os"
	"syscall"
)

func transactionFileOwnerMatches(info os.FileInfo, file BackupFile) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == file.UID && int(stat.Gid) == file.GID
}

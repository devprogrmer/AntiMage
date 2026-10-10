//go:build !linux

package system

import "os"

func transactionFileOwnerMatches(os.FileInfo, BackupFile) bool { return false }

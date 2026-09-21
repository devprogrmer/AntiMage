package vpnuimigration

type Account struct {
	SourceID    int64
	Username    string
	Enabled     bool
	UsedBytes   int64
	DataLimit   *int64
	Expire      *int64
	IPLimit     int64
	DeviceLimit int64
	Note        string
}

type Analysis struct {
	Accounts []Account `json:"-"`
	Warnings []string  `json:"warnings"`
}

type Result struct {
	Detected int      `json:"detected"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Renamed  int      `json:"renamed"`
	Warnings []string `json:"warnings"`
}

type Error struct{ Message string }

func (e Error) Error() string { return e.Message }

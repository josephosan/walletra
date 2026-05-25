package models

import "time"

type Role string

const (
	RoleUser      Role = "user"
	RoleSuperUser Role = "superuser"
)

type Frequency string

const (
	FreqHourly  Frequency = "hourly"
	FreqDaily   Frequency = "daily"
	FreqMonthly Frequency = "monthly"
	FreqYearly  Frequency = "yearly"
)

type User struct {
	ID         string
	TelegramID int64
	Username   string
	Role       Role
}

type Wallet struct {
	ID           string
	UserID       string
	Name         string
	Address      string
	Chain        string
	BaseCoin     string
	IsActive     bool
	LastPolledAt *time.Time
	TokenFilters []string
}

type WalletTransaction struct {
	WalletID     string
	TxHash       string
	Chain        string
	TokenSymbol  string
	TokenAddress string
	Direction    string
	Amount       float64
	AmountUSD    *float64
	Timestamp    time.Time
	RawPayload   []byte
}

type UserSettings struct {
	UserID                  string
	ReportFrequency         Frequency
	IncludeUnchangedWallets bool
	Timezone                string
	NextReportAt            *time.Time
}

type ReportRow struct {
	WalletName string
	Address    string
	Chain      string
	Token      string
	Direction  string
	Amount     float64
}

type UserActivity struct {
	ID              string
	ActorUserID     string
	ActorTelegramID int64
	ActorUsername   string
	ActorRole       Role
	Action          string
	Details         string
	CreatedAt       time.Time
}

type WalletTokenFilter struct {
	TokenSymbol  string
	TokenAddress string
}

type PolygonIndexerState struct {
	Chain            string
	LastIndexedBlock int64
	LastBlockHash    string
}

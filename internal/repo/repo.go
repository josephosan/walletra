package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"walletra/internal/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) UpsertUser(ctx context.Context, telegramID int64, username string) (models.User, bool, error) {
	q := `
INSERT INTO users(telegram_id, username, role)
VALUES ($1,$2,$3)
ON CONFLICT (telegram_id)
DO UPDATE SET username=EXCLUDED.username, updated_at=NOW()
RETURNING id, telegram_id, username, role, (xmax = 0) AS inserted`
	var u models.User
	var inserted bool
	if err := r.db.QueryRow(ctx, q, telegramID, username, models.RoleUser).Scan(&u.ID, &u.TelegramID, &u.Username, &u.Role, &inserted); err != nil {
		return u, false, err
	}
	_, _ = r.db.Exec(ctx, `INSERT INTO user_settings(user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, u.ID)
	return u, inserted, nil
}

func (r *Repository) EnsureSuperUserByTelegramID(ctx context.Context, telegramID int64) (bool, error) {
	cmd, err := r.db.Exec(ctx, `UPDATE users SET role='superuser', updated_at=NOW() WHERE telegram_id=$1`, telegramID)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() > 0, nil
}

func (r *Repository) GetUserByTelegramID(ctx context.Context, telegramID int64) (models.User, error) {
	var u models.User
	err := r.db.QueryRow(ctx, `SELECT id, telegram_id, username, role FROM users WHERE telegram_id=$1`, telegramID).
		Scan(&u.ID, &u.TelegramID, &u.Username, &u.Role)
	return u, err
}

func (r *Repository) AddWallet(ctx context.Context, userID, name, address, chain, baseCoin string, tokens []string) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var walletID string
	if err := tx.QueryRow(ctx, `
INSERT INTO wallets(user_id,name,address,chain,base_coin)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (user_id,address,chain) DO UPDATE SET name=EXCLUDED.name, base_coin=EXCLUDED.base_coin, updated_at=NOW()
RETURNING id`, userID, name, address, chain, baseCoin).Scan(&walletID); err != nil {
		return err
	}
	for _, t := range tokens {
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_token_filters(wallet_id, token_symbol) VALUES ($1,$2) ON CONFLICT (wallet_id, token_symbol) DO NOTHING`, walletID, t); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) ListWalletsByUser(ctx context.Context, userID string) ([]models.Wallet, error) {
	rows, err := r.db.Query(ctx, `SELECT id,user_id,name,address,chain,COALESCE(base_coin,''),is_active,last_polled_at FROM wallets WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	wallets := make([]models.Wallet, 0)
	for rows.Next() {
		var w models.Wallet
		if err := rows.Scan(&w.ID, &w.UserID, &w.Name, &w.Address, &w.Chain, &w.BaseCoin, &w.IsActive, &w.LastPolledAt); err != nil {
			return nil, err
		}
		wallets = append(wallets, w)
	}
	return wallets, rows.Err()
}

func (r *Repository) DeleteWalletsByName(ctx context.Context, userID, walletName string) (int64, error) {
	cmd, err := r.db.Exec(ctx, `DELETE FROM wallets WHERE user_id=$1 AND LOWER(name)=LOWER($2)`, userID, walletName)
	if err != nil {
		return 0, err
	}
	return cmd.RowsAffected(), nil
}

func (r *Repository) CountWalletsByUser(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM wallets WHERE user_id=$1`, userID).Scan(&n)
	return n, err
}

func (r *Repository) AddUserActivity(ctx context.Context, actorUserID string, actorTelegramID int64, actorUsername string, actorRole models.Role, action, details string) error {
	_, err := r.db.Exec(ctx, `
INSERT INTO user_activities(actor_user_id, actor_telegram_id, actor_username, actor_role, action, details)
VALUES ($1,$2,$3,$4,$5,$6)`,
		nullableUUID(actorUserID), actorTelegramID, actorUsername, actorRole, action, details)
	return err
}

func (r *Repository) ListUsersPaginated(ctx context.Context, page, pageSize int) ([]models.User, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize

	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx, `
SELECT id, telegram_id, username, role
FROM users
ORDER BY created_at DESC
LIMIT $1 OFFSET $2`, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]models.User, 0, pageSize)
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.Role); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func (r *Repository) ListUserActivities(ctx context.Context, userTelegramID *int64, hourStart *time.Time, page, pageSize int) ([]models.UserActivity, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	where := make([]string, 0, 2)
	args := make([]any, 0, 6)
	argPos := 1

	if userTelegramID != nil {
		where = append(where, fmt.Sprintf("actor_telegram_id=$%d", argPos))
		args = append(args, *userTelegramID)
		argPos++
	}
	if hourStart != nil {
		where = append(where, fmt.Sprintf("created_at >= $%d AND created_at < $%d", argPos, argPos+1))
		args = append(args, *hourStart, hourStart.Add(time.Hour))
		argPos += 2
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	countSQL := "SELECT COUNT(*) FROM user_activities" + whereSQL
	var total int
	if err := r.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, pageSize, offset)
	listSQL := fmt.Sprintf(`
SELECT id, COALESCE(actor_user_id::text,''), actor_telegram_id, COALESCE(actor_username,''), actor_role, action, COALESCE(details,''), created_at
FROM user_activities%s
ORDER BY created_at DESC
LIMIT $%d OFFSET $%d`, whereSQL, argPos, argPos+1)
	rows, err := r.db.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]models.UserActivity, 0, pageSize)
	for rows.Next() {
		var a models.UserActivity
		if err := rows.Scan(&a.ID, &a.ActorUserID, &a.ActorTelegramID, &a.ActorUsername, &a.ActorRole, &a.Action, &a.Details, &a.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

func (r *Repository) ListWalletsForPolling(ctx context.Context) ([]models.Wallet, error) {
	rows, err := r.db.Query(ctx, `SELECT id,user_id,name,address,chain,COALESCE(base_coin,''),is_active,last_polled_at FROM wallets WHERE is_active=true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	wallets := make([]models.Wallet, 0)
	for rows.Next() {
		var w models.Wallet
		if err := rows.Scan(&w.ID, &w.UserID, &w.Name, &w.Address, &w.Chain, &w.BaseCoin, &w.IsActive, &w.LastPolledAt); err != nil {
			return nil, err
		}
		wallets = append(wallets, w)
	}
	return wallets, rows.Err()
}

func (r *Repository) GetWalletTokens(ctx context.Context, walletID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT token_symbol FROM wallet_token_filters WHERE wallet_id=$1`, walletID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) GetWalletTokenFilters(ctx context.Context, walletID string) ([]models.WalletTokenFilter, error) {
	rows, err := r.db.Query(ctx, `SELECT token_symbol, COALESCE(token_address, '') FROM wallet_token_filters WHERE wallet_id=$1`, walletID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.WalletTokenFilter, 0)
	for rows.Next() {
		var f models.WalletTokenFilter
		if err := rows.Scan(&f.TokenSymbol, &f.TokenAddress); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *Repository) GetPolygonIndexerState(ctx context.Context, chain string) (models.PolygonIndexerState, error) {
	var s models.PolygonIndexerState
	err := r.db.QueryRow(ctx, `SELECT chain, last_indexed_block, COALESCE(last_block_hash,'') FROM polygon_indexer_state WHERE chain=$1`, chain).
		Scan(&s.Chain, &s.LastIndexedBlock, &s.LastBlockHash)
	return s, err
}

func (r *Repository) UpsertPolygonIndexerState(ctx context.Context, chain string, lastBlock int64, blockHash string) error {
	_, err := r.db.Exec(ctx, `
INSERT INTO polygon_indexer_state(chain, last_indexed_block, last_block_hash)
VALUES ($1,$2,$3)
ON CONFLICT (chain)
DO UPDATE SET last_indexed_block=EXCLUDED.last_indexed_block, last_block_hash=EXCLUDED.last_block_hash, updated_at=NOW()`,
		chain, lastBlock, blockHash)
	return err
}

func (r *Repository) InsertWalletTransactions(ctx context.Context, txs []models.WalletTransaction) error {
	for _, t := range txs {
		_, err := r.db.Exec(ctx, `
INSERT INTO wallet_transactions(wallet_id,tx_hash,chain,token_symbol,token_address,direction,amount,amount_usd,tx_timestamp,raw_payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT DO NOTHING`,
			t.WalletID, t.TxHash, t.Chain, t.TokenSymbol, t.TokenAddress, t.Direction, t.Amount, t.AmountUSD, t.Timestamp, t.RawPayload)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) MarkWalletPolled(ctx context.Context, walletID string, at time.Time) error {
	_, err := r.db.Exec(ctx, `UPDATE wallets SET last_polled_at=$2, updated_at=NOW() WHERE id=$1`, walletID, at)
	return err
}

func (r *Repository) GetSettings(ctx context.Context, userID string) (models.UserSettings, error) {
	var s models.UserSettings
	err := r.db.QueryRow(ctx, `SELECT user_id,report_frequency,include_unchanged_wallets,timezone,next_report_at FROM user_settings WHERE user_id=$1`, userID).
		Scan(&s.UserID, &s.ReportFrequency, &s.IncludeUnchangedWallets, &s.Timezone, &s.NextReportAt)
	return s, err
}

func (r *Repository) UpdateSettings(ctx context.Context, userID string, frequency models.Frequency, includeUnchanged bool) error {
	_, err := r.db.Exec(ctx, `
UPDATE user_settings
SET report_frequency=$2, include_unchanged_wallets=$3, updated_at=NOW(), next_report_at=NULL
WHERE user_id=$1`, userID, frequency, includeUnchanged)
	return err
}

func (r *Repository) UsersDueForReport(ctx context.Context, now time.Time) ([]models.User, error) {
	rows, err := r.db.Query(ctx, `
SELECT u.id, u.telegram_id, u.username, u.role
FROM users u
JOIN user_settings s ON s.user_id=u.id
WHERE s.next_report_at IS NULL OR s.next_report_at <= $1`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.Role); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (r *Repository) SetNextReportAt(ctx context.Context, userID string, next time.Time) error {
	_, err := r.db.Exec(ctx, `UPDATE user_settings SET next_report_at=$2, updated_at=NOW() WHERE user_id=$1`, userID, next)
	return err
}

func (r *Repository) AggregateReport(ctx context.Context, userID string, from, to time.Time, includeUnchanged bool) ([]models.ReportRow, error) {
	q := `
SELECT w.name, w.address, w.chain,
       COALESCE(t.token_symbol, '-'),
       COALESCE(t.direction, '-'),
       COALESCE(SUM(t.amount)::float8, 0)
FROM wallets w
LEFT JOIN wallet_transactions t
  ON t.wallet_id=w.id
 AND t.tx_timestamp >= $2
 AND t.tx_timestamp < $3
WHERE w.user_id=$1
GROUP BY w.name, w.address, w.chain, t.token_symbol, t.direction
ORDER BY w.name, t.token_symbol`
	rows, err := r.db.Query(ctx, q, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.ReportRow, 0)
	for rows.Next() {
		var rrow models.ReportRow
		if err := rows.Scan(&rrow.WalletName, &rrow.Address, &rrow.Chain, &rrow.Token, &rrow.Direction, &rrow.Amount); err != nil {
			return nil, err
		}
		if !includeUnchanged && rrow.Amount == 0 {
			continue
		}
		out = append(out, rrow)
	}
	return out, rows.Err()
}

func EncodePayload(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func IsNotFound(err error) bool {
	return err == pgx.ErrNoRows
}

func Wrap(msg string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}

func nullableUUID(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

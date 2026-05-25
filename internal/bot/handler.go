package bot

import (
	"context"
	"fmt"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"walletra/internal/models"
	"walletra/internal/repo"
	"walletra/internal/service"
)

type Handler struct {
	log    *log.Logger
	repo   *repo.Repository
	report *service.ReportService
	super  int64

	mu      sync.Mutex
	states  map[int64]*walletCreateState
	removal map[int64]*removeWalletState
}

type walletCreateState struct {
	Step            int
	Name            string
	Address         string
	Chain           string
	Coin            string
	Tokens          []string
	CustomTokenMode bool
}

type removeWalletState struct {
	Name string
}

var supportedChains = []string{
	"matic-mainnet",
}

var supportedBaseCoins = []string{
	"MATIC",
}

func NewHandler(log *log.Logger, repo *repo.Repository, report *service.ReportService, superUserID int64) *Handler {
	return &Handler{
		log:     log,
		repo:    repo,
		report:  report,
		super:   superUserID,
		states:  map[int64]*walletCreateState{},
		removal: map[int64]*removeWalletState{},
	}
}

func (h *Handler) MainMenu() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("👛 Wallets"), tgbotapi.NewKeyboardButton("📊 Reports")),
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("⚙️ Settings"), tgbotapi.NewKeyboardButton("👤 Profile")),
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("ℹ️ Help")),
	)
}

func (h *Handler) HandleUpdate(ctx context.Context, bot *tgbotapi.BotAPI, upd tgbotapi.Update) {
	if upd.Message != nil {
		h.log.Printf("incoming message chat_id=%d text=%q", upd.Message.Chat.ID, upd.Message.Text)
		h.handleMessage(ctx, bot, upd.Message)
		return
	}
	if upd.CallbackQuery != nil {
		h.log.Printf("incoming callback chat_id=%d data=%q", upd.CallbackQuery.Message.Chat.ID, upd.CallbackQuery.Data)
		h.handleCallback(ctx, bot, upd.CallbackQuery)
	}
}

func (h *Handler) handleMessage(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	username := ""
	if msg.From != nil {
		username = msg.From.UserName
	}
	u, isNewUser, err := h.repo.UpsertUser(ctx, msg.Chat.ID, username)
	if err != nil {
		h.log.Printf("upsert user failed chat_id=%d err=%v", msg.Chat.ID, err)
		h.sendText(bot, msg.Chat.ID, "Could not initialize user.")
		return
	}
	if msg.Chat.ID == h.super {
		if _, err := h.repo.EnsureSuperUserByTelegramID(ctx, msg.Chat.ID); err != nil {
			h.log.Printf("ensure superuser failed chat_id=%d err=%v", msg.Chat.ID, err)
		}
		u, _ = h.repo.GetUserByTelegramID(ctx, msg.Chat.ID)
	}
	h.log.Printf("user loaded chat_id=%d user_id=%s role=%s", msg.Chat.ID, u.ID, u.Role)
	if isNewUser {
		h.notifySuperuserNewSignup(bot, u)
	}
	h.audit(ctx, u, "message", msg.Text)

	if h.handleWalletWizard(ctx, bot, msg, u.ID) {
		return
	}
	if h.handleWalletRemovalInput(bot, msg) {
		return
	}

	switch msg.Text {
	case "/start":
		h.log.Printf("command /start chat_id=%d", msg.Chat.ID)
		r := tgbotapi.NewMessage(msg.Chat.ID, "🚀 Walletra Bot is ready.\n\nUse the menu below.")
		r.ReplyMarkup = h.MainMenu()
		_, _ = bot.Send(r)
	case "👛 Wallets", "Wallets":
		h.sendWalletMenu(bot, msg.Chat.ID)
	case "📊 Reports", "Reports":
		h.sendReportMenu(bot, msg.Chat.ID)
	case "⚙️ Settings", "Settings":
		h.sendSettingsMenu(ctx, bot, msg.Chat.ID, u.ID)
	case "👤 Profile", "Profile":
		h.sendProfile(ctx, bot, msg.Chat.ID, u)
	case "ℹ️ Help", "Help":
		h.sendText(
			bot,
			msg.Chat.ID,
			fmt.Sprintf(
				"ℹ️ Use `👛 Wallets` to add tracked wallets, `📊 Reports` for on-demand reports, and `⚙️ Settings` to control delivery and visibility.\n\n🌐 Supported chains:\n%s\n\n🪙 Supported base coins:\n%s",
				bulletList(supportedChains),
				bulletList(supportedBaseCoins),
			),
		)
	default:
		if strings.HasPrefix(msg.Text, "/") {
			if h.handleSlashCommand(ctx, bot, msg, u) {
				return
			}
		}
		h.log.Printf("unhandled message chat_id=%d text=%q", msg.Chat.ID, msg.Text)
		h.sendText(bot, msg.Chat.ID, "❓ Use /start or the menu buttons.\n\nUse /help for command help.")
	}
}

func (h *Handler) handleWalletWizard(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, userID string) bool {
	h.mu.Lock()
	st, ok := h.states[msg.Chat.ID]
	h.mu.Unlock()
	if !ok {
		return false
	}

	text := strings.TrimSpace(msg.Text)
	if isCancelText(text) {
		h.mu.Lock()
		delete(h.states, msg.Chat.ID)
		h.mu.Unlock()
		h.log.Printf("wallet wizard cancelled chat_id=%d user_id=%s", msg.Chat.ID, userID)
		h.sendText(bot, msg.Chat.ID, "🛑 Wallet setup canceled.")
		return true
	}
	h.log.Printf("wallet wizard step=%d chat_id=%d input=%q", st.Step, msg.Chat.ID, text)
	switch st.Step {
	case 1:
		st.Name = text
		st.Step = 2
		h.sendText(bot, msg.Chat.ID, "📬 Send wallet address.\n\nType `cancel` anytime to stop.")
	case 2:
		st.Address = text
		st.Step = 3
		h.sendChainPicker(bot, msg.Chat.ID)
	case 5:
		if !st.CustomTokenMode {
			h.sendText(bot, msg.Chat.ID, "Use token buttons below.")
			return true
		}
		st.CustomTokenMode = false
		custom := splitCSV(text)
		st.Tokens = uniqTokens(append(st.Tokens, custom...))
		h.finishWalletCreate(ctx, bot, msg.Chat.ID, userID, st)
		h.mu.Lock()
		delete(h.states, msg.Chat.ID)
		h.mu.Unlock()
	}
	return true
}

func (h *Handler) finishWalletCreate(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, userID string, st *walletCreateState) {
	err := h.repo.AddWallet(ctx, userID, st.Name, st.Address, st.Chain, st.Coin, st.Tokens)
	if err != nil {
		h.log.Printf("wallet create failed user_id=%s name=%q address=%q chain=%q err=%v", userID, st.Name, st.Address, st.Chain, err)
		h.sendText(bot, chatID, "❌ Failed to save wallet.")
		return
	}
	h.log.Printf("wallet created user_id=%s name=%q address=%q chain=%q tokens=%v", userID, st.Name, st.Address, st.Chain, st.Tokens)
	h.sendText(bot, chatID, "✅ Wallet saved and added to tracking.")
}

func uniqTokens(tokens []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		x := strings.ToUpper(strings.TrimSpace(t))
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

func (h *Handler) sendChainPicker(bot *tgbotapi.BotAPI, chatID int64) {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(supportedChains))
	for _, c := range supportedChains {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🌐 "+c, "wallet_chain:"+c),
		))
	}
	msg := tgbotapi.NewMessage(chatID, "Choose chain:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	_, _ = bot.Send(msg)
}

func (h *Handler) sendCoinPicker(bot *tgbotapi.BotAPI, chatID int64) {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(supportedBaseCoins))
	for _, c := range supportedBaseCoins {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🪙 "+c, "wallet_coin:"+c),
		))
	}
	msg := tgbotapi.NewMessage(chatID, "Choose base coin:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	_, _ = bot.Send(msg)
}

func (h *Handler) sendTokenPicker(bot *tgbotapi.BotAPI, chatID int64, st *walletCreateState) {
	common := []string{"USDT", "USDC", "WETH", "WBTC", "LINK", "PEPE"}
	selected := map[string]bool{}
	for _, t := range st.Tokens {
		selected[t] = true
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(common)+2)
	for _, t := range common {
		label := "⚪ " + t
		if selected[t] {
			label = "🟢 " + t
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, "wallet_tok_toggle:"+t),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✍️ Add Custom Tokens", "wallet_tok_custom"),
	))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Done", "wallet_tok_done"),
		tgbotapi.NewInlineKeyboardButtonData("⏭ Skip", "wallet_tok_skip"),
	))
	msg := tgbotapi.NewMessage(chatID, "Choose tokens to track:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	_, _ = bot.Send(msg)
}

func (h *Handler) updateTokenPickerMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, st *walletCreateState) {
	common := []string{"USDT", "USDC", "WETH", "WBTC", "LINK", "PEPE"}
	selected := map[string]bool{}
	for _, t := range st.Tokens {
		selected[t] = true
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(common)+2)
	for _, t := range common {
		label := "⚪ " + t
		if selected[t] {
			label = "🟢 " + t
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, "wallet_tok_toggle:"+t),
		))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✍️ Add Custom Tokens", "wallet_tok_custom"),
	))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Done", "wallet_tok_done"),
		tgbotapi.NewInlineKeyboardButtonData("⏭ Skip", "wallet_tok_skip"),
	))
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	edit := tgbotapi.NewEditMessageText(chatID, messageID, "Choose tokens to track:")
	edit.ReplyMarkup = &kb
	if _, err := bot.Send(edit); err != nil {
		msg := tgbotapi.NewMessage(chatID, "Choose tokens to track:")
		msg.ReplyMarkup = kb
		_, _ = bot.Send(msg)
	}
}

func (h *Handler) toggleToken(st *walletCreateState, token string) {
	token = strings.ToUpper(strings.TrimSpace(token))
	if token == "" {
		return
	}
	next := make([]string, 0, len(st.Tokens))
	found := false
	for _, t := range st.Tokens {
		if t == token {
			found = true
			continue
		}
		next = append(next, t)
	}
	if !found {
		next = append(next, token)
	}
	st.Tokens = next
}

func (h *Handler) getWalletState(chatID int64) (*walletCreateState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.states[chatID]
	return st, ok
}

func (h *Handler) clearWalletState(chatID int64) {
	h.mu.Lock()
	delete(h.states, chatID)
	h.mu.Unlock()
}

func (h *Handler) handleWalletRemovalInput(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) bool {
	h.mu.Lock()
	_, waiting := h.removal[msg.Chat.ID]
	h.mu.Unlock()
	if !waiting {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	if isCancelText(text) {
		h.mu.Lock()
		delete(h.removal, msg.Chat.ID)
		h.mu.Unlock()
		h.sendText(bot, msg.Chat.ID, "🛑 Wallet removal canceled.")
		return true
	}
	if text == "" {
		h.sendText(bot, msg.Chat.ID, "❌ Wallet name cannot be empty. Send wallet name or type `cancel`.")
		return true
	}
	h.mu.Lock()
	h.removal[msg.Chat.ID] = &removeWalletState{Name: text}
	h.mu.Unlock()

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Yes, remove", "wallet_remove_confirm"),
			tgbotapi.NewInlineKeyboardButtonData("❌ No, cancel", "wallet_remove_cancel"),
		),
	)
	msgOut := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("⚠️ Remove wallet(s) named `%s`?\n\nThis action cannot be undone.", text))
	msgOut.ReplyMarkup = kb
	_, _ = bot.Send(msgOut)
	return true
}

func isSupported(list []string, value string) bool {
	return slices.Contains(list, value)
}

func bulletList(items []string) string {
	var sb strings.Builder
	for _, v := range items {
		sb.WriteString("• ")
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func (h *Handler) handleCallback(ctx context.Context, bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	username := ""
	if cb.From != nil {
		username = cb.From.UserName
	}
	u, isNewUser, err := h.repo.UpsertUser(ctx, chatID, username)
	if err != nil {
		h.log.Printf("upsert user failed callback chat_id=%d err=%v", chatID, err)
		h.sendText(bot, chatID, "Could not initialize user.")
		return
	}
	if chatID == h.super {
		if _, err := h.repo.EnsureSuperUserByTelegramID(ctx, chatID); err != nil {
			h.log.Printf("ensure superuser failed chat_id=%d err=%v", chatID, err)
		}
		u, _ = h.repo.GetUserByTelegramID(ctx, chatID)
	}
	if isNewUser {
		h.notifySuperuserNewSignup(bot, u)
	}
	data := cb.Data
	h.audit(ctx, u, "callback", data)
	switch {
	case strings.HasPrefix(data, "wallet_chain:"):
		st, ok := h.getWalletState(chatID)
		if !ok || st.Step < 3 {
			h.sendText(bot, chatID, "ℹ️ Start with Add Wallet first.")
			break
		}
		chain := strings.TrimPrefix(data, "wallet_chain:")
		if !isSupported(supportedChains, chain) {
			h.sendText(bot, chatID, "❌ Unsupported chain.")
			break
		}
		st.Chain = chain
		st.Step = 4
		h.sendCoinPicker(bot, chatID)
	case strings.HasPrefix(data, "wallet_coin:"):
		st, ok := h.getWalletState(chatID)
		if !ok || st.Step < 4 {
			h.sendText(bot, chatID, "ℹ️ Choose chain first.")
			break
		}
		coin := strings.TrimPrefix(data, "wallet_coin:")
		if !isSupported(supportedBaseCoins, coin) {
			h.sendText(bot, chatID, "❌ Unsupported base coin.")
			break
		}
		st.Coin = coin
		st.Step = 5
		h.sendTokenPicker(bot, chatID, st)
	case strings.HasPrefix(data, "wallet_tok_toggle:"):
		st, ok := h.getWalletState(chatID)
		if !ok || st.Step < 5 {
			h.sendText(bot, chatID, "ℹ️ Start with Add Wallet first.")
			break
		}
		token := strings.TrimPrefix(data, "wallet_tok_toggle:")
		h.toggleToken(st, token)
		h.updateTokenPickerMessage(bot, chatID, cb.Message.MessageID, st)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "Updated"))
		return
	case data == "wallet_tok_custom":
		st, ok := h.getWalletState(chatID)
		if !ok || st.Step < 5 {
			h.sendText(bot, chatID, "ℹ️ Start with Add Wallet first.")
			break
		}
		st.CustomTokenMode = true
		h.sendText(bot, chatID, "✍️ Send custom tokens as CSV (example: TOKEN1,TOKEN2).")
	case data == "wallet_tok_skip":
		st, ok := h.getWalletState(chatID)
		if !ok {
			h.sendText(bot, chatID, "ℹ️ No active wallet setup.")
			break
		}
		h.finishWalletCreate(ctx, bot, chatID, u.ID, st)
		h.clearWalletState(chatID)
	case data == "wallet_tok_done":
		st, ok := h.getWalletState(chatID)
		if !ok {
			h.sendText(bot, chatID, "ℹ️ No active wallet setup.")
			break
		}
		h.finishWalletCreate(ctx, bot, chatID, u.ID, st)
		h.clearWalletState(chatID)
	case strings.HasPrefix(data, "admin_users_page:"):
		if u.Role != models.RoleSuperUser {
			h.sendText(bot, chatID, "⛔ Superuser only.")
			break
		}
		pageStr := strings.TrimPrefix(data, "admin_users_page:")
		page, err := strconv.Atoi(pageStr)
		if err != nil || page < 1 {
			page = 1
		}
		h.sendAdminUsersPage(ctx, bot, chatID, page)
	case data == "wallet_add":
		h.log.Printf("wallet_add clicked chat_id=%d", chatID)
		h.mu.Lock()
		h.states[chatID] = &walletCreateState{Step: 1}
		h.mu.Unlock()
		h.sendCancelablePrompt(bot, chatID, "📝 Send wallet name")
	case data == "wallet_remove":
		h.mu.Lock()
		h.removal[chatID] = &removeWalletState{}
		h.mu.Unlock()
		h.sendText(bot, chatID, "🗑 Send wallet name to remove.\n\nType `cancel` to stop.")
	case data == "wallet_remove_cancel":
		h.mu.Lock()
		delete(h.removal, chatID)
		h.mu.Unlock()
		h.sendText(bot, chatID, "🛑 Wallet removal canceled.")
	case data == "wallet_remove_confirm":
		h.mu.Lock()
		rs, ok := h.removal[chatID]
		delete(h.removal, chatID)
		h.mu.Unlock()
		if !ok || strings.TrimSpace(rs.Name) == "" {
			h.sendText(bot, chatID, "ℹ️ No wallet removal request found.")
			break
		}
		count, err := h.repo.DeleteWalletsByName(ctx, u.ID, rs.Name)
		if err != nil {
			h.sendText(bot, chatID, "❌ Failed to remove wallet.")
			break
		}
		if count == 0 {
			h.sendText(bot, chatID, fmt.Sprintf("ℹ️ No wallet found with name `%s`.", rs.Name))
		} else {
			h.sendText(bot, chatID, fmt.Sprintf("✅ Removed %d wallet(s) named `%s`.", count, rs.Name))
		}
	case data == "wallet_cancel":
		st, exists := h.getWalletState(chatID)
		_ = st
		h.clearWalletState(chatID)
		if exists {
			h.log.Printf("wallet wizard cancelled via button chat_id=%d user_id=%s", chatID, u.ID)
			h.sendText(bot, chatID, "🛑 Wallet setup canceled.")
		} else {
			h.sendText(bot, chatID, "ℹ️ No active wallet setup.")
		}
	case data == "wallet_list":
		h.log.Printf("wallet_list clicked chat_id=%d", chatID)
		wallets, err := h.repo.ListWalletsByUser(ctx, u.ID)
		if err != nil {
			h.log.Printf("wallet list failed user_id=%s err=%v", u.ID, err)
			h.sendText(bot, chatID, "❌ Failed to load wallets")
			break
		}
		h.log.Printf("wallet list user_id=%s count=%d", u.ID, len(wallets))
		if len(wallets) == 0 {
			h.sendText(bot, chatID, "👛 No wallets yet.")
			break
		}
		var sb strings.Builder
		sb.WriteString("👛 Your wallets:\n")
		for _, w := range wallets {
			sb.WriteString(fmt.Sprintf("• %s | %s | %s\n", w.Name, w.Chain, w.Address))
		}
		h.sendText(bot, chatID, sb.String())
	case strings.HasPrefix(data, "report_all:"):
		freq := models.Frequency(strings.TrimPrefix(data, "report_all:"))
		h.log.Printf("manual all-wallet report requested user_id=%s freq=%s", u.ID, freq)
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err != nil {
			h.sendText(bot, chatID, "❌ Failed to read settings")
			break
		}
		text, err := h.report.BuildReportText(ctx, u.ID, freq, settings.IncludeUnchangedWallets, time.Now().UTC())
		if err != nil {
			h.sendText(bot, chatID, "❌ Failed to generate report")
			break
		}
		h.sendText(bot, chatID, text)
	case strings.HasPrefix(data, "report_wallet:"):
		parts := strings.SplitN(strings.TrimPrefix(data, "report_wallet:"), ":", 2)
		if len(parts) != 2 {
			h.sendText(bot, chatID, "❌ Invalid wallet report request.")
			break
		}
		freq := models.Frequency(parts[0])
		walletID := parts[1]
		h.log.Printf("manual wallet report requested user_id=%s wallet_id=%s freq=%s", u.ID, walletID, freq)
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err != nil {
			h.sendText(bot, chatID, "❌ Failed to read settings")
			break
		}
		text, err := h.report.BuildWalletReportText(ctx, u.ID, walletID, freq, settings.IncludeUnchangedWallets, time.Now().UTC())
		if err != nil {
			h.sendText(bot, chatID, "❌ Failed to generate wallet report")
			break
		}
		h.sendText(bot, chatID, text)
	case strings.HasPrefix(data, "report_"):
		freq := models.Frequency(strings.TrimPrefix(data, "report_"))
		h.sendWalletReportPicker(ctx, bot, chatID, u.ID, freq)
	case strings.HasPrefix(data, "freq_"):
		freq := models.Frequency(strings.TrimPrefix(data, "freq_"))
		h.log.Printf("frequency update requested user_id=%s freq=%s", u.ID, freq)
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err == nil {
			err = h.repo.UpdateSettings(ctx, u.ID, freq, settings.IncludeUnchangedWallets)
		}
		if err != nil {
			h.log.Printf("frequency update failed user_id=%s err=%v", u.ID, err)
			h.sendText(bot, chatID, "❌ Could not update frequency")
			break
		}
		h.log.Printf("frequency updated user_id=%s freq=%s", u.ID, freq)
		h.updateSettingsMessage(ctx, bot, chatID, cb.Message.MessageID, u.ID)
		_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("✅ Frequency: %s", freq)))
		return
	case data == "toggle_unchanged":
		h.log.Printf("toggle unchanged requested user_id=%s", u.ID)
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err != nil {
			h.log.Printf("toggle unchanged failed read settings user_id=%s err=%v", u.ID, err)
			h.sendText(bot, chatID, "❌ Could not read settings")
			break
		}
		err = h.repo.UpdateSettings(ctx, u.ID, settings.ReportFrequency, !settings.IncludeUnchangedWallets)
		if err != nil {
			h.log.Printf("toggle unchanged failed update user_id=%s err=%v", u.ID, err)
			h.sendText(bot, chatID, "❌ Could not update setting")
			break
		}
		h.log.Printf("toggle unchanged updated user_id=%s new_value=%t", u.ID, !settings.IncludeUnchangedWallets)
		h.updateSettingsMessage(ctx, bot, chatID, cb.Message.MessageID, u.ID)
		if !settings.IncludeUnchangedWallets {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "✅ Include unchanged: ON"))
		} else {
			_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "✅ Include unchanged: OFF"))
		}
		return
	}

	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "ok"))
}

func (h *Handler) handleSlashCommand(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, user models.User) bool {
	text := strings.TrimSpace(msg.Text)
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	cmd := parts[0]

	switch cmd {
	case "/help":
		h.sendHelp(bot, msg.Chat.ID)
		h.audit(ctx, user, "cmd_help", "")
		return true
	case "/profile":
		h.sendProfile(ctx, bot, msg.Chat.ID, user)
		h.audit(ctx, user, "cmd_profile", "")
		return true
	case "/admin", "/admin_help":
		if user.Role != models.RoleSuperUser {
			h.sendText(bot, msg.Chat.ID, "⛔ Superuser only.")
			return true
		}
		h.sendAdminHelp(bot, msg.Chat.ID)
		h.audit(ctx, user, "cmd_admin_help", "")
		return true
	case "/admin_users":
		if user.Role != models.RoleSuperUser {
			h.sendText(bot, msg.Chat.ID, "⛔ Superuser only.")
			return true
		}
		page := 1
		if len(parts) >= 2 {
			if p, err := strconv.Atoi(parts[1]); err == nil && p > 0 {
				page = p
			}
		}
		h.sendAdminUsersPage(ctx, bot, msg.Chat.ID, page)
		h.audit(ctx, user, "cmd_admin_users", fmt.Sprintf("page=%d", page))
		return true
	case "/admin_activity":
		if user.Role != models.RoleSuperUser {
			h.sendText(bot, msg.Chat.ID, "⛔ Superuser only.")
			return true
		}
		h.sendAdminActivities(ctx, bot, msg.Chat.ID, parts[1:])
		h.audit(ctx, user, "cmd_admin_activity", strings.Join(parts[1:], " "))
		return true
	default:
		return false
	}
}

func (h *Handler) sendWalletMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ Add Wallet", "wallet_add"),
			tgbotapi.NewInlineKeyboardButtonData("📜 List Wallets", "wallet_list"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🗑 Remove Wallet", "wallet_remove"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🛑 Cancel Setup", "wallet_cancel"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, "👛 Wallet menu\n\nChoose an action:")
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

func (h *Handler) sendReportMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🕐 Hourly", "report_hourly"),
			tgbotapi.NewInlineKeyboardButtonData("📅 Daily", "report_daily"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🗓️ Monthly", "report_monthly"),
			tgbotapi.NewInlineKeyboardButtonData("📆 Yearly", "report_yearly"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, "📊 Choose report period:\n")
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

func (h *Handler) sendWalletReportPicker(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, userID string, freq models.Frequency) {
	wallets, err := h.repo.ListWalletsByUser(ctx, userID)
	if err != nil {
		h.sendText(bot, chatID, "❌ Failed to load wallets for report.")
		return
	}
	if len(wallets) == 0 {
		h.sendText(bot, chatID, "👛 No wallets available. Add a wallet first.")
		return
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(wallets)+1)
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("📦 All Wallets", "report_all:"+string(freq)),
	))
	for _, w := range wallets {
		label := fmt.Sprintf("👛 %s", w.Name)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, "report_wallet:"+string(freq)+":"+w.ID),
		))
	}
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Select wallet for %s report:", freq))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	_, _ = bot.Send(msg)
}

func (h *Handler) sendSettingsMenu(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, userID string) {
	settings, err := h.repo.GetSettings(ctx, userID)
	if err != nil {
		h.sendText(bot, chatID, "❌ Could not load settings")
		return
	}
	text, kb := h.buildSettingsView(settings)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

func (h *Handler) updateSettingsMessage(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, messageID int, userID string) {
	settings, err := h.repo.GetSettings(ctx, userID)
	if err != nil {
		h.sendText(bot, chatID, "❌ Could not load settings")
		return
	}
	text, kb := h.buildSettingsView(settings)
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.ReplyMarkup = &kb
	if _, err := bot.Send(edit); err != nil {
		// Fallback to sending a fresh settings message if edit fails.
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ReplyMarkup = kb
		_, _ = bot.Send(msg)
	}
}

func (h *Handler) buildSettingsView(settings models.UserSettings) (string, tgbotapi.InlineKeyboardMarkup) {
	freqLabel := func(freq models.Frequency, label string) string {
		if settings.ReportFrequency == freq {
			return "🟢 " + label
		}
		return "⚪ " + label
	}
	unchangedLabel := "🔴 Include Unchanged: OFF"
	if settings.IncludeUnchangedWallets {
		unchangedLabel = "🟢 Include Unchanged: ON"
	}

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(freqLabel(models.FreqHourly, "Auto Hourly"), "freq_hourly"),
			tgbotapi.NewInlineKeyboardButtonData(freqLabel(models.FreqDaily, "Auto Daily"), "freq_daily"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(freqLabel(models.FreqMonthly, "Auto Monthly"), "freq_monthly"),
			tgbotapi.NewInlineKeyboardButtonData(freqLabel(models.FreqYearly, "Auto Yearly"), "freq_yearly"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(unchangedLabel, "toggle_unchanged"),
		),
	)
	return "⚙️ Settings\n\n🟢 Active | ⚪ Inactive", kb
}

func (h *Handler) sendText(bot *tgbotapi.BotAPI, chatID int64, text string) {
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, text))
}

func (h *Handler) sendHelp(bot *tgbotapi.BotAPI, chatID int64) {
	h.sendText(bot, chatID, "📘 Help Center\n\n/start - Start bot\n/profile - Your profile\n/help - This help\n\nIf you are superuser, use /admin.")
}

func (h *Handler) sendAdminHelp(bot *tgbotapi.BotAPI, chatID int64) {
	h.sendText(bot, chatID, "🛡️ Superuser Help Center\n\n/admin - Open this help\n/admin_users [page] - List users with pagination\n/admin_activity - Recent activities (all users)\n/admin_activity <telegram_id> - Activities for one user\n/admin_activity <telegram_id> <YYYY-MM-DDTHH> - One user activities in specific hour (UTC)\n\nExample:\n/admin_activity 123456789 2026-05-24T18")
}

func (h *Handler) sendAdminUsersPage(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, page int) {
	const pageSize = 10
	users, total, err := h.repo.ListUsersPaginated(ctx, page, pageSize)
	if err != nil {
		h.sendText(bot, chatID, "❌ Failed to load users.")
		return
	}
	totalPages := int(math.Ceil(float64(total) / float64(pageSize)))
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("👥 Registered Users (page %d/%d)\n\n", page, totalPages))
	for _, u := range users {
		un := u.Username
		if un == "" {
			un = "(no username)"
		}
		sb.WriteString(fmt.Sprintf("• tg:%d | @%s | role:%s\n", u.TelegramID, un, u.Role))
	}
	if len(users) == 0 {
		sb.WriteString("No users found.\n")
	}

	rows := []tgbotapi.InlineKeyboardButton{}
	if page > 1 {
		rows = append(rows, tgbotapi.NewInlineKeyboardButtonData("⬅️ Prev", fmt.Sprintf("admin_users_page:%d", page-1)))
	}
	if page < totalPages {
		rows = append(rows, tgbotapi.NewInlineKeyboardButtonData("Next ➡️", fmt.Sprintf("admin_users_page:%d", page+1)))
	}

	msg := tgbotapi.NewMessage(chatID, sb.String())
	if len(rows) > 0 {
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(rows...))
	}
	_, _ = bot.Send(msg)
}

func (h *Handler) sendAdminActivities(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, args []string) {
	var tgID *int64
	var hour *time.Time

	if len(args) >= 1 {
		v, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			h.sendText(bot, chatID, "❌ Invalid telegram id.\nUse: /admin_activity <telegram_id> <YYYY-MM-DDTHH>")
			return
		}
		tgID = &v
	}
	if len(args) >= 2 {
		t, err := time.Parse("2006-01-02T15", args[1])
		if err != nil {
			h.sendText(bot, chatID, "❌ Invalid hour format.\nUse UTC hour like: 2026-05-24T18")
			return
		}
		tt := t.UTC()
		hour = &tt
	}

	activities, total, err := h.repo.ListUserActivities(ctx, tgID, hour, 1, 50)
	if err != nil {
		h.sendText(bot, chatID, "❌ Failed to load activities.")
		return
	}

	var header strings.Builder
	header.WriteString("📜 User Activities\n")
	if tgID != nil {
		header.WriteString(fmt.Sprintf("User: %d\n", *tgID))
	}
	if hour != nil {
		header.WriteString(fmt.Sprintf("Hour(UTC): %s\n", hour.Format("2006-01-02T15")))
	}
	header.WriteString(fmt.Sprintf("Total: %d\n\n", total))

	var body strings.Builder
	max := len(activities)
	if max > 20 {
		max = 20
	}
	for i := 0; i < max; i++ {
		a := activities[i]
		body.WriteString(fmt.Sprintf("• %s | tg:%d | %s\n  action: %s\n  details: %s\n", a.CreatedAt.UTC().Format(time.RFC3339), a.ActorTelegramID, a.ActorRole, a.Action, a.Details))
	}
	if len(activities) == 0 {
		body.WriteString("No activities found.")
	}
	h.sendText(bot, chatID, header.String()+body.String())
}

func (h *Handler) sendProfile(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, user models.User) {
	settings, err := h.repo.GetSettings(ctx, user.ID)
	if err != nil {
		h.sendText(bot, chatID, "❌ Could not load profile settings.")
		return
	}
	walletCount, err := h.repo.CountWalletsByUser(ctx, user.ID)
	if err != nil {
		h.sendText(bot, chatID, "❌ Could not load profile wallets.")
		return
	}
	username := user.Username
	if username == "" {
		username = "(not set)"
	}
	profile := fmt.Sprintf(
		"👤 Profile\n\nTelegram ID: %d\nUsername: @%s\nRole: %s\n\nWallets: %d\nAuto report: %s\nInclude unchanged: %t\nTimezone: %s",
		user.TelegramID,
		username,
		user.Role,
		walletCount,
		settings.ReportFrequency,
		settings.IncludeUnchangedWallets,
		settings.Timezone,
	)
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, profile))
}

func (h *Handler) audit(ctx context.Context, user models.User, action, details string) {
	if err := h.repo.AddUserActivity(ctx, user.ID, user.TelegramID, user.Username, user.Role, action, details); err != nil {
		h.log.Printf("activity log failed user_id=%s action=%s err=%v", user.ID, action, err)
	}
}

func (h *Handler) notifySuperuserNewSignup(bot *tgbotapi.BotAPI, user models.User) {
	if h.super <= 0 {
		return
	}
	username := user.Username
	if strings.TrimSpace(username) == "" {
		username = "(not set)"
	}
	text := fmt.Sprintf(
		"🆕 New user signed up\n\nTelegram ID: %d\nUsername: @%s\nRole: %s\nUser ID: %s",
		user.TelegramID,
		username,
		user.Role,
		user.ID,
	)
	if _, err := bot.Send(tgbotapi.NewMessage(h.super, text)); err != nil {
		h.log.Printf("notify superuser signup failed super_tg=%d new_tg=%d err=%v", h.super, user.TelegramID, err)
	}
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.ToUpper(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func isCancelText(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "cancel", "/cancel", "stop", "/stop":
		return true
	default:
		return false
	}
}

func (h *Handler) sendCancelablePrompt(bot *tgbotapi.BotAPI, chatID int64, text string) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🛑 Cancel Setup", "wallet_cancel"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, text+"\n\nType `cancel` anytime to stop.")
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

package bot

import (
	"context"
	"fmt"
	"log"
	"slices"
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

	mu     sync.Mutex
	states map[int64]*walletCreateState
}

type walletCreateState struct {
	Step    int
	Name    string
	Address string
	Chain   string
	Coin    string
}

var supportedChains = []string{
	"btc-mainnet",
	"eth-mainnet",
	"bsc-mainnet",
	"base-mainnet",
	"matic-mainnet",
	"arbitrum-mainnet",
	"optimism-mainnet",
	"avalanche-mainnet",
	"fantom-mainnet",
}

var supportedBaseCoins = []string{
	"BTC",
	"ETH",
	"BNB",
	"BASE",
	"MATIC",
	"ARB",
	"OP",
	"AVAX",
	"FTM",
}

func NewHandler(log *log.Logger, repo *repo.Repository, report *service.ReportService, superUserID int64) *Handler {
	return &Handler{log: log, repo: repo, report: report, super: superUserID, states: map[int64]*walletCreateState{}}
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
	u, err := h.repo.UpsertUser(ctx, msg.Chat.ID, username)
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

	if h.handleWalletWizard(ctx, bot, msg, u.ID) {
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
		h.log.Printf("unhandled message chat_id=%d text=%q", msg.Chat.ID, msg.Text)
		h.sendText(bot, msg.Chat.ID, "❓ Use /start or the menu buttons.")
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
		h.sendText(bot, msg.Chat.ID, fmt.Sprintf("🌐 Send chain (supported only):\n\n%s", bulletList(supportedChains)))
	case 3:
		chain := strings.ToLower(text)
		if !isSupported(supportedChains, chain) {
			h.sendText(bot, msg.Chat.ID, fmt.Sprintf("❌ Unsupported chain.\n\nChoose one of:\n\n%s", bulletList(supportedChains)))
			return true
		}
		st.Chain = chain
		st.Step = 4
		h.sendText(bot, msg.Chat.ID, fmt.Sprintf("🪙 Send base coin (supported only):\n\n%s", bulletList(supportedBaseCoins)))
	case 4:
		coin := strings.ToUpper(text)
		if !isSupported(supportedBaseCoins, coin) {
			h.sendText(bot, msg.Chat.ID, fmt.Sprintf("❌ Unsupported base coin.\n\nChoose one of:\n\n%s", bulletList(supportedBaseCoins)))
			return true
		}
		st.Coin = coin
		st.Step = 5
		h.sendText(bot, msg.Chat.ID, "🧾 Send token symbols to track.\n\nExample: `PEPE,USDT,LINK`")
	case 5:
		tokens := splitCSV(text)
		err := h.repo.AddWallet(ctx, userID, st.Name, st.Address, st.Chain, st.Coin, tokens)
		h.mu.Lock()
		delete(h.states, msg.Chat.ID)
		h.mu.Unlock()
		if err != nil {
			h.log.Printf("wallet create failed user_id=%s name=%q address=%q chain=%q err=%v", userID, st.Name, st.Address, st.Chain, err)
			h.sendText(bot, msg.Chat.ID, "❌ Failed to save wallet.")
			return true
		}
		h.log.Printf("wallet created user_id=%s name=%q address=%q chain=%q tokens=%v", userID, st.Name, st.Address, st.Chain, tokens)
		h.sendText(bot, msg.Chat.ID, "✅ Wallet saved and added to tracking.")
	}
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
	u, err := h.repo.UpsertUser(ctx, chatID, username)
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

	data := cb.Data
	switch {
	case data == "wallet_add":
		h.log.Printf("wallet_add clicked chat_id=%d", chatID)
		h.mu.Lock()
		h.states[chatID] = &walletCreateState{Step: 1}
		h.mu.Unlock()
		h.sendCancelablePrompt(bot, chatID, "📝 Send wallet name")
	case data == "wallet_cancel":
		h.mu.Lock()
		_, exists := h.states[chatID]
		delete(h.states, chatID)
		h.mu.Unlock()
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
	case strings.HasPrefix(data, "report_"):
		freq := models.Frequency(strings.TrimPrefix(data, "report_"))
		h.log.Printf("manual report requested user_id=%s freq=%s", u.ID, freq)
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err != nil {
			h.log.Printf("get settings failed user_id=%s err=%v", u.ID, err)
			h.sendText(bot, chatID, "❌ Failed to read settings")
			break
		}
		text, err := h.report.BuildReportText(ctx, u.ID, freq, settings.IncludeUnchangedWallets, time.Now().UTC())
		if err != nil {
			h.log.Printf("build report failed user_id=%s freq=%s err=%v", u.ID, freq, err)
			h.sendText(bot, chatID, "❌ Failed to generate report")
			break
		}
		h.sendText(bot, chatID, text)
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
		h.sendText(bot, chatID, fmt.Sprintf("✅ Report frequency changed to %s", freq))
		h.sendSettingsMenu(ctx, bot, chatID, u.ID)
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
		if !settings.IncludeUnchangedWallets {
			h.sendText(bot, chatID, "✅ Include unchanged wallets: ON")
		} else {
			h.sendText(bot, chatID, "✅ Include unchanged wallets: OFF")
		}
		h.sendSettingsMenu(ctx, bot, chatID, u.ID)
	}

	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "ok"))
}

func (h *Handler) sendWalletMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ Add Wallet", "wallet_add"),
			tgbotapi.NewInlineKeyboardButtonData("📜 List Wallets", "wallet_list"),
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

func (h *Handler) sendSettingsMenu(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, userID string) {
	settings, err := h.repo.GetSettings(ctx, userID)
	if err != nil {
		h.sendText(bot, chatID, "❌ Could not load settings")
		return
	}
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
	msg := tgbotapi.NewMessage(chatID, "⚙️ Settings\n\n🟢 Active | ⚪ Inactive")
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

func (h *Handler) sendText(bot *tgbotapi.BotAPI, chatID int64, text string) {
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, text))
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

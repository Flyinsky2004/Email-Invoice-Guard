package telegram

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"email-invoice-guard/internal/core"
)

// menuFolderButtonLimit keeps the folder keyboard well below Telegram's
// 100-button ceiling; every folder is still listed in the message text.
const menuFolderButtonLimit = 50

func (b *Bot) menuSettingsReply(ctx context.Context) (reply, error) {
	tr := b.tr(ctx)
	timezone := b.cfg.Timezone
	if v, err := b.store.GetSetting(ctx, settingTimezone); err == nil && v != "" {
		timezone = v
	}
	reminder := b.cfg.ReminderTime
	if v, err := b.store.GetSetting(ctx, settingReminderTime); err == nil && v != "" {
		reminder = v
	}
	interval := int(b.cfg.SyncInterval / time.Second)
	if v, err := b.store.GetSetting(ctx, settingSyncInterval); err == nil {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			interval = n
		}
	}
	lines := []string{
		tr.t("settings.timezoneLine", firstNonEmpty(timezone, "UTC")),
		tr.t("settings.reminderLine", firstNonEmpty(reminder, "09:00")),
		tr.t("settings.syncLine", interval),
		tr.t("settings.languageLine", string(b.language(ctx))),
	}
	rows := [][]inlineButton{
		{
			{Text: tr.t("menu.button.langEN"), CallbackData: menuCallbackData(menuLanguage, "en", "")},
			{Text: tr.t("menu.button.langZH"), CallbackData: menuCallbackData(menuLanguage, "zh-CN", "")},
		},
		{
			{Text: tr.t("menu.button.timezone"), CallbackData: menuCallbackData(menuTimezone, "", "")},
			{Text: tr.t("menu.button.reminderTime"), CallbackData: menuCallbackData(menuReminderTime, "", "")},
		},
		{
			{Text: tr.t("menu.button.syncInterval"), CallbackData: menuCallbackData(menuSyncInterval, "", "")},
			{Text: tr.t("menu.button.folders"), CallbackData: menuCallbackData(menuFolders, "", "")},
		},
		menuNavRow(tr, "", ""),
	}
	return renderList(tr.t("menu.settings.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

// menuFoldersReply lists the accounts (no param) or one account's folders.
func (b *Bot) menuFoldersReply(ctx context.Context, userID int64, accountID string) (reply, error) {
	tr := b.tr(ctx)
	if accountID == "" {
		accounts, err := b.store.ListAccounts(ctx)
		if err != nil {
			return reply{}, fmt.Errorf("list accounts: %w", err)
		}
		owned := make([]core.MailAccount, 0, len(accounts))
		for _, a := range accounts {
			if a.OwnerUserID == userID {
				owned = append(owned, a)
			}
		}
		lines := []string{tr.t("menu.folders.accounts")}
		rows := make([][]inlineButton, 0, len(owned)+1)
		for i, a := range owned {
			if i >= maxAccountLines {
				lines = append(lines, tr.t("accounts.more", len(owned)-maxAccountLines))
				break
			}
			state := tr.t("common.enabled")
			if !a.Enabled {
				state = tr.t("common.disabled")
			}
			lines = append(lines, tr.t("accounts.line", a.Address, string(a.Provider), state, a.ID))
			rows = append(rows, []inlineButton{{
				Text:         menuButtonText(a.Address, 60),
				CallbackData: menuCallbackData(menuFolders, a.ID, ""),
			}})
		}
		rows = append(rows, menuNavRow(tr, menuSettings, ""))
		return renderList(tr.t("menu.folders.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
	}

	account, err := b.store.GetAccount(ctx, accountID)
	if err != nil || account.OwnerUserID != userID {
		return reply{
			parts:    []string{tr.t("error.accountNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuSettings, "")}},
		}, nil
	}
	folders, err := b.store.ListFolders(ctx, accountID)
	if err != nil {
		return reply{}, fmt.Errorf("list folders: %w", err)
	}
	lines := []string{tr.t("settings.foldersTitle", account.Address)}
	rows := make([][]inlineButton, 0, len(folders)+1)
	if len(folders) == 0 {
		lines = append(lines, tr.t("settings.folderEmpty", account.Address))
	}
	for i, folder := range folders {
		state := tr.t("common.disabled")
		if folder.Enabled {
			state = tr.t("common.enabled")
		}
		name := folder.Name
		if name == "" {
			name = folder.Role
		}
		lines = append(lines, tr.t("settings.folderItem", name, folder.ID, state))
		if i >= menuFolderButtonLimit || folder.ID == "" {
			continue
		}
		rows = append(rows, []inlineButton{{
			Text:         tr.t("menu.folderButton", menuButtonText(name, 48), state),
			CallbackData: menuCallbackData(menuFolderToggle, accountID, folderToken(folder.ID)),
		}})
	}
	if len(folders) > menuFolderButtonLimit {
		lines = append(lines, tr.t("menu.folders.more", len(folders)-menuFolderButtonLimit))
	}
	rows = append(rows, menuNavRow(tr, menuFolders, ""))
	return renderList(tr.t("menu.folders.title"), lines, &inlineKeyboard{InlineKeyboard: rows}), nil
}

// menuFolderToggleAction flips one folder's sync flag. The callback carries a
// stable token derived from the folder ID, so a reordered or refreshed folder
// list cannot redirect the press to a different folder.
func (b *Bot) menuFolderToggleAction(ctx context.Context, cq *callbackQuery, accountID, sub string) (*reply, string, bool, error) {
	tr := b.tr(ctx)
	account, err := b.store.GetAccount(ctx, accountID)
	if err != nil || account.OwnerUserID != cq.From.ID {
		return &reply{
			parts:    []string{tr.t("error.accountNotFound")},
			keyboard: &inlineKeyboard{InlineKeyboard: [][]inlineButton{menuNavRow(tr, menuSettings, "")}},
		}, "", false, nil
	}
	folders, err := b.store.ListFolders(ctx, accountID)
	if err != nil {
		return nil, "", false, fmt.Errorf("list folders: %w", err)
	}
	var folder *core.MailFolder
	for i := range folders {
		if folderToken(folders[i].ID) == sub {
			folder = &folders[i]
			break
		}
	}
	if folder == nil {
		return nil, tr.t("error.folderNotFound"), true, nil
	}
	folder.Enabled = !folder.Enabled
	if err := b.store.SaveFolder(ctx, *folder); err != nil {
		return nil, "", false, fmt.Errorf("save folder: %w", err)
	}
	state := tr.t("common.disabled")
	if folder.Enabled {
		state = tr.t("common.enabled")
	}
	rep, err := b.menuFoldersReply(ctx, cq.From.ID, accountID)
	if err != nil {
		return nil, "", false, err
	}
	return &rep, tr.t("settings.folderSet", folder.Name, folder.ID, state), false, nil
}

// folderToken derives a stable short callback token from a folder ID. The
// listing order can change between render and press, so an index is not safe.
func folderToken(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const (
	settingTimezone     = "timezone"
	settingReminderTime = "reminder_time"
	settingSyncInterval = "sync_interval_seconds"
)

func (b *Bot) commandSettings(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if len(args) == 0 {
		return b.showSettings(ctx)
	}
	switch strings.ToLower(args[0]) {
	case "timezone":
		return b.setTimezone(ctx, args[1:])
	case "reminder":
		return b.setReminderTime(ctx, args[1:])
	case "sync":
		return b.setSyncInterval(ctx, args[1:])
	case "folder":
		return b.setFolder(ctx, args[1:], userID)
	case "language":
		return b.setLanguage(ctx, args[1:])
	default:
		return reply{parts: []string{tr.t("settings.usage")}}, nil
	}
}

func (b *Bot) showSettings(ctx context.Context) (reply, error) {
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
		tr.t("settings.folderLine"),
	}
	return renderList(tr.t("settings.title"), lines, nil), nil
}

func (b *Bot) setTimezone(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	if len(args) != 1 {
		return reply{parts: []string{tr.t("settings.timezoneUsage")}}, nil
	}
	name := strings.TrimSpace(args[0])
	if _, err := time.LoadLocation(name); err != nil {
		return reply{parts: []string{tr.t("settings.timezoneInvalid")}}, nil
	}
	if err := b.store.SetSetting(ctx, settingTimezone, name); err != nil {
		return reply{}, fmt.Errorf("save timezone: %w", err)
	}
	return reply{parts: []string{tr.t("settings.timezoneSet", escape(name))}}, nil
}

func (b *Bot) setReminderTime(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	if len(args) != 1 {
		return reply{parts: []string{tr.t("settings.reminderUsage")}}, nil
	}
	value := strings.TrimSpace(args[0])
	if _, err := time.Parse("15:04", value); err != nil {
		return reply{parts: []string{tr.t("settings.reminderInvalid")}}, nil
	}
	if err := b.store.SetSetting(ctx, settingReminderTime, value); err != nil {
		return reply{}, fmt.Errorf("save reminder time: %w", err)
	}
	return reply{parts: []string{tr.t("settings.reminderSet", escape(value))}}, nil
}

func (b *Bot) setSyncInterval(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	if len(args) != 1 {
		return reply{parts: []string{tr.t("settings.syncUsage")}}, nil
	}
	seconds, err := parsePositiveSeconds(args[0])
	if err != nil {
		return reply{parts: []string{tr.t("settings.syncInvalid")}}, nil
	}
	if err := b.store.SetSetting(ctx, settingSyncInterval, strconv.Itoa(seconds)); err != nil {
		return reply{}, fmt.Errorf("save sync interval: %w", err)
	}
	return reply{parts: []string{tr.t("settings.syncSet", seconds)}}, nil
}

// setLanguage shows or changes the display language. The confirmation is
// rendered in the newly selected language so the change is immediately visible.
func (b *Bot) setLanguage(ctx context.Context, args []string) (reply, error) {
	tr := b.tr(ctx)
	if len(args) == 0 {
		return reply{parts: []string{tr.t("settings.languageCurrent", string(b.language(ctx)))}}, nil
	}
	if len(args) != 1 {
		return reply{parts: []string{tr.t("settings.languageUsage")}}, nil
	}
	lg, ok := parseLanguage(args[0])
	if !ok {
		return reply{parts: []string{tr.t("settings.languageInvalid")}}, nil
	}
	if err := b.store.SetSetting(ctx, settingLanguage, string(lg)); err != nil {
		return reply{}, fmt.Errorf("save language: %w", err)
	}
	return reply{parts: []string{translator{lang: lg}.t("settings.languageSet", string(lg))}}, nil
}

func (b *Bot) setFolder(ctx context.Context, args []string, userID int64) (reply, error) {
	tr := b.tr(ctx)
	if len(args) == 0 {
		return reply{parts: []string{tr.t("settings.folderUsage")}}, nil
	}
	account, err := b.store.GetAccount(ctx, args[0])
	if err != nil {
		return reply{parts: []string{tr.t("error.accountNotFound")}}, nil
	}
	if account.OwnerUserID != userID {
		return reply{parts: []string{tr.t("error.accountNotFound")}}, nil
	}

	if len(args) == 1 {
		folders, err := b.store.ListFolders(ctx, args[0])
		if err != nil {
			return reply{}, fmt.Errorf("list folders: %w", err)
		}
		if len(folders) == 0 {
			return reply{parts: []string{
				tr.t("settings.folderEmpty", escape(account.Address)),
			}}, nil
		}
		lines := make([]string, 0, len(folders))
		for _, folder := range folders {
			state := tr.t("common.disabled")
			if folder.Enabled {
				state = tr.t("common.enabled")
			}
			name := folder.Name
			if name == "" {
				name = folder.Role
			}
			lines = append(lines, tr.t("settings.folderItem", name, folder.ID, state))
		}
		return renderList(tr.t("settings.foldersTitle", account.Address), lines, nil), nil
	}

	if len(args) != 3 {
		return reply{parts: []string{tr.t("settings.folderUsage")}}, nil
	}
	folderID, state := args[1], strings.ToLower(args[2])
	if state != "on" && state != "off" {
		return reply{parts: []string{tr.t("settings.folderState")}}, nil
	}
	folders, err := b.store.ListFolders(ctx, args[0])
	if err != nil {
		return reply{}, fmt.Errorf("list folders: %w", err)
	}
	var folder *core.MailFolder
	for i := range folders {
		if folders[i].ID == folderID {
			folder = &folders[i]
			break
		}
	}
	if folder == nil {
		return reply{parts: []string{tr.t("error.folderNotFound")}}, nil
	}
	folder.Enabled = state == "on"
	if err := b.store.SaveFolder(ctx, *folder); err != nil {
		return reply{}, fmt.Errorf("save folder: %w", err)
	}
	return reply{parts: []string{tr.t("settings.folderSet", escape(folder.Name), escape(folder.ID), state)}}, nil
}

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
	default:
		return reply{parts: []string{"Usage: /settings [timezone <tz> | reminder <HH:MM> | sync <seconds> | folder <accountID> [<folderID> on|off]]"}}, nil
	}
}

func (b *Bot) showSettings(ctx context.Context) (reply, error) {
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
		"timezone: " + firstNonEmpty(timezone, "UTC"),
		"reminder_time: " + firstNonEmpty(reminder, "09:00"),
		fmt.Sprintf("sync_interval_seconds: %d", interval),
		"folder: /settings folder <accountID> [<folderID> on|off]",
	}
	return renderList("Settings", lines, nil), nil
}

func (b *Bot) setTimezone(ctx context.Context, args []string) (reply, error) {
	if len(args) != 1 {
		return reply{parts: []string{"Usage: /settings timezone <IANA name>"}}, nil
	}
	name := strings.TrimSpace(args[0])
	if _, err := time.LoadLocation(name); err != nil {
		return reply{parts: []string{"Invalid timezone. Use an IANA name such as Asia/Taipei."}}, nil
	}
	if err := b.store.SetSetting(ctx, settingTimezone, name); err != nil {
		return reply{}, fmt.Errorf("save timezone: %w", err)
	}
	return reply{parts: []string{"Timezone set to " + escape(name) + "."}}, nil
}

func (b *Bot) setReminderTime(ctx context.Context, args []string) (reply, error) {
	if len(args) != 1 {
		return reply{parts: []string{"Usage: /settings reminder <HH:MM>"}}, nil
	}
	value := strings.TrimSpace(args[0])
	if _, err := time.Parse("15:04", value); err != nil {
		return reply{parts: []string{"Invalid reminder time. Use HH:MM in 24-hour form."}}, nil
	}
	if err := b.store.SetSetting(ctx, settingReminderTime, value); err != nil {
		return reply{}, fmt.Errorf("save reminder time: %w", err)
	}
	return reply{parts: []string{"Reminder time set to " + escape(value) + "."}}, nil
}

func (b *Bot) setSyncInterval(ctx context.Context, args []string) (reply, error) {
	if len(args) != 1 {
		return reply{parts: []string{"Usage: /settings sync <seconds>"}}, nil
	}
	seconds, err := parsePositiveSeconds(args[0])
	if err != nil {
		return reply{parts: []string{"Invalid interval. Use a positive number of seconds (max 86400)."}}, nil
	}
	if err := b.store.SetSetting(ctx, settingSyncInterval, strconv.Itoa(seconds)); err != nil {
		return reply{}, fmt.Errorf("save sync interval: %w", err)
	}
	return reply{parts: []string{fmt.Sprintf("Sync interval set to %d seconds.", seconds)}}, nil
}

func (b *Bot) setFolder(ctx context.Context, args []string, userID int64) (reply, error) {
	if len(args) == 0 {
		return reply{parts: []string{"Usage: /settings folder <accountID> [<folderID> on|off]"}}, nil
	}
	account, err := b.store.GetAccount(ctx, args[0])
	if err != nil {
		return reply{parts: []string{"Account not found."}}, nil
	}
	if account.OwnerUserID != userID {
		return reply{parts: []string{"Account not found."}}, nil
	}

	if len(args) == 1 {
		folders, err := b.store.ListFolders(ctx, args[0])
		if err != nil {
			return reply{}, fmt.Errorf("list folders: %w", err)
		}
		if len(folders) == 0 {
			return reply{parts: []string{
				"No folders stored for " + escape(account.Address) + ". Run /sync first to discover folders.",
			}}, nil
		}
		lines := make([]string, 0, len(folders))
		for _, folder := range folders {
			state := "disabled"
			if folder.Enabled {
				state = "enabled"
			}
			name := folder.Name
			if name == "" {
				name = folder.Role
			}
			lines = append(lines, fmt.Sprintf("• %s id=%s (%s)", name, folder.ID, state))
		}
		return renderList("Folders for "+account.Address, lines, nil), nil
	}

	if len(args) != 3 {
		return reply{parts: []string{"Usage: /settings folder <accountID> [<folderID> on|off]"}}, nil
	}
	folderID, state := args[1], strings.ToLower(args[2])
	if state != "on" && state != "off" {
		return reply{parts: []string{"Folder state must be on or off."}}, nil
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
		return reply{parts: []string{"Folder not found for this account."}}, nil
	}
	folder.Enabled = state == "on"
	if err := b.store.SaveFolder(ctx, *folder); err != nil {
		return reply{}, fmt.Errorf("save folder: %w", err)
	}
	return reply{parts: []string{fmt.Sprintf("Folder %s (%s) is now %s.", escape(folder.Name), escape(folder.ID), state)}}, nil
}

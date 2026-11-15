// Package telegram implements the Telegram long-polling bot, outbox delivery
// and authorized command/callback handling.
//
// User-visible text is bilingual (English and Simplified Chinese) and selected
// by the stored "language" setting. The exported FormatEmail and FormatInvoice
// helpers keep their original signatures and always render English for external
// acceptance; the Bot uses language-aware internal variants.
package telegram

import (
	"context"
	"fmt"
	"strings"

	"email-invoice-guard/internal/core"
)

// lang is a supported display language.
type lang string

const (
	langEN lang = "en"
	langZH lang = "zh-CN"
)

// settingLanguage is the dynamic settings key holding the display language.
const settingLanguage = "language"

// translator renders catalog text for one language. Catalog strings are trusted
// static text; any dynamic argument is supplied by the caller. Lines written
// through the chunker are escaped as a whole by writeRaw, while lines sent
// directly (parse_mode=HTML) must have their dynamic arguments escaped by the
// caller.
type translator struct{ lang lang }

// t returns the catalog string for key, formatted with args when provided. A
// missing key returns the key itself so tests fail loudly instead of silently
// falling back to English.
func (tr translator) t(key string, args ...any) string {
	catalog := catalogs[tr.lang]
	if catalog == nil {
		catalog = catalogs[langZH]
	}
	format, ok := catalog[key]
	if !ok {
		return key
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

func (tr translator) invoiceStatus(status core.InvoiceStatus) string {
	switch status {
	case core.PendingConfirmation:
		return tr.t("invoice.status.pending")
	case core.PaidConfirmed:
		return tr.t("invoice.status.paid")
	case core.ReviewRequired:
		return tr.t("invoice.status.review")
	case core.Cancelled:
		return tr.t("invoice.status.cancelled")
	default:
		return string(status)
	}
}

func (tr translator) planStatus(status core.ManualPlanStatus) string {
	switch status {
	case core.ManualPlanActive:
		return tr.t("manual.planStatus.active")
	case core.ManualPlanPaused:
		return tr.t("manual.planStatus.paused")
	case core.ManualPlanCancelled:
		return tr.t("manual.planStatus.cancelled")
	default:
		return string(status)
	}
}

func (tr translator) occurrenceStatus(status core.ManualOccurrenceStatus) string {
	switch status {
	case core.ManualOccurrencePending:
		return tr.t("manual.occStatus.pending")
	case core.ManualOccurrencePaid:
		return tr.t("manual.occStatus.paid")
	case core.ManualOccurrenceCancelled:
		return tr.t("manual.occStatus.cancelled")
	default:
		return string(status)
	}
}

// parseLanguage normalizes a user-provided language token. zh, zh-cn and cn are
// accepted aliases for zh-CN.
func parseLanguage(raw string) (lang, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "en":
		return langEN, true
	case "zh", "zh-cn", "cn":
		return langZH, true
	default:
		return "", false
	}
}

// language resolves the display language for the current update. The setting is
// read on every call; it is never cached across updates. A missing setting, a
// read error or an invalid stored value falls back to zh-CN; an invalid stored
// value is logged once per read.
func (b *Bot) language(ctx context.Context) lang {
	if b.store == nil {
		return langZH
	}
	value, err := b.store.GetSetting(ctx, settingLanguage)
	if err != nil {
		return langZH
	}
	if lg, ok := parseLanguage(value); ok {
		return lg
	}
	if b.log != nil && strings.TrimSpace(value) != "" {
		b.log.Warn("telegram: invalid language setting; falling back to zh-CN", "value", value)
	}
	return langZH
}

// tr resolves the translator for the current update.
func (b *Bot) tr(ctx context.Context) translator { return translator{lang: b.language(ctx)} }

// text resolves the display language and returns one catalog string.
func (b *Bot) text(ctx context.Context, key string, args ...any) string {
	return b.tr(ctx).t(key, args...)
}

var catalogs = map[lang]map[string]string{
	langEN: catalogEN,
	langZH: catalogZH,
}

var catalogEN = map[string]string{
	// common
	"common.unknown":       "unknown",
	"common.uncategorized": "uncategorized",
	"common.enabled":       "enabled",
	"common.disabled":      "disabled",

	// errors
	"error.unauthorized":         "Not authorized",
	"error.unauthorizedSentence": "Not authorized.",
	"error.unknownAction":        "Unknown action",
	"error.commandFailed":        "Command failed. Check the server logs for details.",
	"error.accountNotFound":      "Account not found.",
	"error.invoiceNotFound":      "Invoice not found",
	"error.mailNotFound":         "Mail not found",
	"error.assetNotFound":        "Asset not found",
	"error.reminderNotFound":     "Reminder not found.",
	"error.folderNotFound":       "Folder not found for this account.",
	"error.invoiceConflict":      "Cannot change this invoice in its current state",
	"error.reminderConflict":     "Cannot change this reminder in its current state.",
	"error.reminderInvalid":      "Invalid reminder details.",
	"error.paymentUpdate":        "Payment update failed",
	"error.removalFailed":        "Removal failed",
	"error.planLookup":           "Plan lookup failed",
	"error.historyLookup":        "History lookup failed",
	"error.reminderUpdate":       "Reminder update failed",
	"error.manualNotConfigured":  "Manual reminders are not configured.",
	"error.noInvoiceHistory":     "No invoice history",
	"error.invoiceNotPending":    "Invoice is not pending confirmation",

	// start and help
	"start.welcome":       "Invoice Guard is ready. Use /help to see available commands.",
	"help.unknownCommand": "Unknown command. Use /help.",
	"help.body": `<b>Invoice Guard commands</b>
/start - welcome
/help - this help
/add microsoft|qq - connect a mailbox
/accounts - list connected accounts
/remove &lt;accountID&gt; [confirm] - disable an account
/sync - run a mailbox sync now
/latest - latest 5 mails
/search &lt;text&gt; - search stored mail
/invoices [pending|paid|overdue|review] - list invoices
/pending /paid /overdue - invoice shortcuts
/assets [provider] - list tracked assets
/providers - supported sources and parsers
/status - system counters
/settings - show or update settings
/settings folder &lt;accountID&gt; - list folders for an account
/settings folder &lt;accountID&gt; &lt;folderID&gt; on|off - enable or disable a folder`,
	"help.manualBody": `<b>Manual recurring reminders</b>
/reminder_add - create a recurring bill with the wizard
/reminders - list manual plans
/reminder &lt;id&gt; - show one plan and its pending occurrences
/reminder_edit &lt;id&gt; - edit a plan with the wizard
/reminder_pause &lt;id&gt; - pause a plan
/reminder_resume &lt;id&gt; - resume a paused plan
/reminder_delete &lt;id&gt; - cancel a plan
/reminder_paid &lt;occurrenceID&gt; - confirm an occurrence paid
/reminder_pending - list pending occurrences
/reminder_history &lt;id&gt; - plan events and dispatches
/cancel - cancel the active wizard`,

	// /add
	"add.usage":       "Usage: /add microsoft|qq",
	"add.unsupported": "Unsupported provider. Use microsoft or qq.",
	"add.noLink":      "Onboarding did not return a link. Check the server logs.",
	"add.openLink":    "Open this one-time link to connect %s:\n%s",

	// accounts
	"accounts.empty":     "No accounts connected. Use /add microsoft|qq.",
	"accounts.more":      "… and %d more",
	"accounts.line":      "• %s (%s, %s) id=%s",
	"accounts.syncError": " sync_error=%s",
	"accounts.title":     "Accounts",

	// remove
	"remove.usage":         "Usage: /remove <accountID> [confirm]",
	"remove.confirmPrompt": "Disable account %s? Historical mail and invoices are retained.",
	"remove.confirmButton": "Confirm removal",
	"remove.disabled":      "Account disabled: %s",
	"remove.disabledShort": "Account disabled",

	// sync
	"sync.notConfigured": "Sync is not configured.",
	"sync.running":       "Sync is already running.",
	"sync.started":       "Sync started in the background.",

	// lists
	"list.empty":               "%s: nothing found.",
	"list.mailLine":            "%d. %s — %s (%s)",
	"latest.title":             "Latest mail",
	"search.usage":             "Usage: /search <text>",
	"search.title":             "Search results",
	"mail.button":              "Mail %d",
	"invoices.title":           "Invoices",
	"invoices.pending":         "Pending invoices",
	"invoices.paid":            "Paid invoices",
	"invoices.review":          "Invoices needing review",
	"invoices.overdue":         "Overdue invoices",
	"invoices.usage":           "Usage: /invoices [pending|paid|overdue|review]",
	"invoices.line":            "%d. %s %s due %s (%s)",
	"invoices.button":          "Invoice %d",
	"assets.empty":             "Assets: nothing found.",
	"assets.title":             "Assets",
	"assets.line":              "%d. %s (%s)",
	"assets.historyButton":     "History %d",
	"providers.sources":        "Sources: microsoft, qq",
	"providers.parser":         "Parser: %s v%s",
	"providers.title":          "Providers",
	"status.accounts":          "Accounts: %d",
	"status.emails":            "Emails: %d",
	"status.pendingInvoices":   "Pending invoices: %d",
	"status.pendingDeliveries": "Pending deliveries: %d",
	"status.title":             "Status",

	// invoice status labels
	"invoice.status.pending":   "PENDING_CONFIRMATION (manual confirmation required)",
	"invoice.status.paid":      "PAID_CONFIRMED (manually confirmed)",
	"invoice.status.review":    "REVIEW_REQUIRED",
	"invoice.status.cancelled": "CANCELLED",

	// callbacks
	"callback.paidPrompt":        "Confirm manual payment for invoice %s (%s)?\nOnly press Confirm if you have actually paid this invoice.",
	"callback.confirmPaid":       "Confirm paid",
	"callback.undo":              "Undo",
	"callback.cancel":            "Cancel",
	"callback.confirmPayment":    "Confirm payment",
	"callback.alreadyPaid":       "Already marked as paid",
	"callback.markedPaid":        "Marked as paid",
	"callback.alreadyPending":    "Already pending",
	"callback.restoredPending":   "Restored to pending",
	"callback.invoiceCancelled":  "Invoice cancelled",
	"callback.openingMail":       "Opening mail",
	"callback.openingInvoice":    "Opening invoice",
	"callback.assetHistory":      "Asset history",
	"callback.assetHistoryTitle": "Asset invoice history",

	// email notification
	"email.title":          "<b>New mail received</b>",
	"email.account":        "Account: %s",
	"email.provider":       "Provider: %s",
	"email.from":           "From: %s",
	"email.vendor":         "Vendor: %s / %s",
	"email.subject":        "Subject: %s",
	"email.time":           "Time: %s",
	"email.noText":         "(no text content)",
	"email.links":          "<b>Links</b>",
	"email.attachments":    "<b>Attachments (%d)</b>",
	"email.bullet":         "• %s",
	"email.attachmentType": " (%s)",
	"email.attachmentSize": " %d bytes",

	// invoice notification
	"invoice.title":          "<b>Invoice</b>",
	"invoice.provider":       "Provider: %s",
	"invoice.account":        "Account: %s",
	"invoice.status":         "Status: %s",
	"invoice.amountDue":      "Amount due: %s",
	"invoice.subtotal":       "Subtotal: %s",
	"invoice.credit":         "Credit: %s",
	"invoice.total":          "Total: %s",
	"invoice.balance":        "Account balance: %s",
	"invoice.created":        "Created: %s",
	"invoice.due":            "Due: %s",
	"invoice.paymentMethod":  "Payment method: %s",
	"invoice.confirmedAt":    "Confirmed at: %s",
	"invoice.item":           "<b>Item %d</b>",
	"invoice.model":          "Model: %s",
	"invoice.host":           "Host: %s",
	"invoice.os":             "OS: %s",
	"invoice.period":         "Period: %s – %s",
	"invoice.originalPrice":  "Original price: %s",
	"invoice.discount":       "Discount: %s",
	"invoice.finalPrice":     "Final price: %s",
	"invoice.promo":          "Promo code: %s",
	"invoice.discountType":   "Discount type: %s",
	"invoice.reviewRequired": "<b>Review required</b>",
	"invoice.manualNote":     "Payment status is manual: only an authorized user can mark this invoice paid.",
	"invoice.dateUnknown":    "?",

	// reminder note
	"reminder.unknownDue": "Reminder: due date unknown.",
	"reminder.dueOn":      "Reminder: due %s.",
	"reminder.dueIn":      "Reminder: due in %d day(s) (%s).",
	"reminder.dueToday":   "Reminder: due today (%s).",
	"reminder.overdue":    "Reminder: overdue by %d day(s) (%s).",

	// settings
	"settings.usage":           "Usage: /settings [timezone <tz> | reminder <HH:MM> | sync <seconds> | folder <accountID> [<folderID> on|off] | language en|zh-CN]",
	"settings.title":           "Settings",
	"settings.timezoneLine":    "timezone: %s",
	"settings.reminderLine":    "reminder_time: %s",
	"settings.syncLine":        "sync_interval_seconds: %d",
	"settings.folderLine":      "folder: /settings folder <accountID> [<folderID> on|off]",
	"settings.languageLine":    "language: %s",
	"settings.timezoneUsage":   "Usage: /settings timezone <IANA name>",
	"settings.timezoneInvalid": "Invalid timezone. Use an IANA name such as Asia/Taipei.",
	"settings.timezoneSet":     "Timezone set to %s.",
	"settings.reminderUsage":   "Usage: /settings reminder <HH:MM>",
	"settings.reminderInvalid": "Invalid reminder time. Use HH:MM in 24-hour form.",
	"settings.reminderSet":     "Reminder time set to %s.",
	"settings.syncUsage":       "Usage: /settings sync <seconds>",
	"settings.syncInvalid":     "Invalid interval. Use a positive number of seconds (max 86400).",
	"settings.syncSet":         "Sync interval set to %d seconds.",
	"settings.folderUsage":     "Usage: /settings folder <accountID> [<folderID> on|off]",
	"settings.folderEmpty":     "No folders stored for %s. Run /sync first to discover folders.",
	"settings.foldersTitle":    "Folders for %s",
	"settings.folderItem":      "• %s id=%s (%s)",
	"settings.folderState":     "Folder state must be on or off.",
	"settings.folderSet":       "Folder %s (%s) is now %s.",
	"settings.languageUsage":   "Usage: /settings language en|zh-CN",
	"settings.languageInvalid": "Invalid language. Use en or zh-CN.",
	"settings.languageSet":     "Language set to %s.",
	"settings.languageCurrent": "Current language: %s.",

	// auth buttons
	"button.viewInvoice":  "View invoice",
	"button.originalMail": "Original mail",
	"button.markPaid":     "Mark paid",
	"button.undoPaid":     "Undo paid",

	// manual commands
	"manual.usagePrefix":          "Usage: %s",
	"manual.noPlans":              "Manual reminders: no plans. Use /reminder_add.",
	"manual.planLine":             "%d. %s — %s, next due %s (id=%s)",
	"manual.planButton":           "Plan %d",
	"manual.title":                "Manual reminders",
	"manual.usage.reminder":       "/reminder <id>",
	"manual.usage.edit":           "/reminder_edit <id>",
	"manual.usage.pause":          "/reminder_pause <id>",
	"manual.usage.resume":         "/reminder_resume <id>",
	"manual.usage.delete":         "/reminder_delete <id>",
	"manual.usage.paid":           "/reminder_paid <occurrenceID>",
	"manual.usage.history":        "/reminder_history <id>",
	"manual.paused":               "Plan paused: %s",
	"manual.resumed":              "Plan resumed: %s, next due %s",
	"manual.cancelled":            "Plan cancelled: %s",
	"manual.occurrencePaid":       "Occurrence due %s marked as paid.",
	"manual.pendingNone":          "Pending manual occurrences: none.",
	"manual.pendingLine":          "%d. %s — due %s, %s (id=%s)",
	"manual.markPaidButton":       "Mark paid %s",
	"manual.undoButton":           "Undo %s",
	"manual.pendingTitle":         "Pending manual occurrences",
	"manual.wizardCancelled":      "Wizard cancelled.",
	"manual.noActiveWizard":       "No active wizard.",
	"manual.historyButton":        "History",
	"manual.viewPlanButton":       "View plan",
	"manual.amountNotFixed":       "amount not fixed",
	"manual.planStatus.active":    "ACTIVE",
	"manual.planStatus.paused":    "PAUSED",
	"manual.planStatus.cancelled": "CANCELLED",
	"manual.occStatus.pending":    "PENDING (unpaid)",
	"manual.occStatus.paid":       "PAID",
	"manual.occStatus.cancelled":  "CANCELLED",

	// manual callbacks
	"manual.occNotPending":  "Occurrence is not pending confirmation",
	"manual.confirmPrompt":  "Confirm manual payment for %s due %s (%s)?\nOnly press Confirm if you have actually paid this occurrence.",
	"manual.openingPlan":    "Opening plan",
	"manual.openingHistory": "Opening history",

	// manual formatting
	"manualFormat.planTitle":        "<b>Manual reminder plan</b>",
	"manualFormat.provider":         "Provider: %s",
	"manualFormat.status":           "Status: %s",
	"manualFormat.amount":           "Amount: %s",
	"manualFormat.billingDay":       "Billing day: %d",
	"manualFormat.reminder":         "Reminder: %s (%s)",
	"manualFormat.nextDue":          "Next due: %s",
	"manualFormat.description":      "Description: %s",
	"manualFormat.notes":            "Notes: %s",
	"manualFormat.paymentURL":       "Payment URL: %s",
	"manualFormat.pendingNone":      "Pending occurrences: none.",
	"manualFormat.pendingHeader":    "<b>Pending occurrences (%d)</b>",
	"manualFormat.occurrenceLine":   "%d. due %s — %s — %s (id=%s)",
	"manualFormat.manualNote":       "Payment status is manual: only an authorized user can mark an occurrence paid.",
	"manualFormat.summaryTitle":     "<b>Manual reminder</b>",
	"manualFormat.planStatus":       "Plan status: %s",
	"manualFormat.occurrenceHeader": "<b>Occurrence %d</b>",
	"manualFormat.due":              "Due: %s",
	"manualFormat.historyTitle":     "<b>Manual reminder history</b>",
	"manualFormat.eventsNone":       "Events: none.",
	"manualFormat.eventsHeader":     "<b>Events (%d)</b>",
	"manualFormat.eventLine":        "• %s at %s",
	"manualFormat.eventOccurrence":  " (occurrence %s)",
	"manualFormat.previewTitle":     "<b>Review manual reminder</b>",
	"manualFormat.title":            "Title: %s",
	"manualFormat.period":           "Period: %s",
	"manualFormat.asset":            "Asset: %s",
	"manualFormat.assetNone":        "Asset: none",
	"manualFormat.paymentURLNone":   "Payment URL: none",
	"manualFormat.confirmToSave":    "Confirm to save this plan.",

	// wizard
	"wizard.expired":               "Wizard expired. Start again with /reminder_add.",
	"wizard.err.title":             "Title must be 1-200 characters.",
	"wizard.err.period":            "Select Monthly to continue; v1 supports monthly plans only.",
	"wizard.err.provider":          "Provider name must be at most 100 characters.",
	"wizard.err.amount":            "Send an amount like 18.00 USD, or - for no fixed amount.",
	"wizard.err.amountInvalid":     "Invalid amount or unsupported currency.",
	"wizard.err.currency":          "Unsupported currency. Select one of the buttons.",
	"wizard.err.billingDay":        "Billing day must be a number from 1 to 31.",
	"wizard.err.asset":             "Select an asset or Skip.",
	"wizard.err.reminderTime":      "Reminder time must be HH:MM in 24-hour form.",
	"wizard.err.timezone":          "Invalid timezone. Use an IANA name such as Asia/Taipei.",
	"wizard.err.paymentURL":        "Payment URL must be an https URL without credentials, a non-443 port or a fragment.",
	"wizard.err.notes":             "Notes must be at most 2000 characters.",
	"wizard.err.confirm":           "Press Confirm, Back or Cancel.",
	"wizard.err.sendAmountFirst":   "Send an amount first.",
	"wizard.err.amountForCurrency": "Invalid amount for %s.",
	"wizard.assetUnavailable":      "Assets are unavailable; press Skip.",
	"wizard.assetLoadError":        "Could not load assets; press Skip.",
	"wizard.step.title":            "Step 1/10 — send the plan title.",
	"wizard.step.period":           "Step 2/10 — select the billing period. v1 supports monthly plans only.",
	"wizard.step.provider":         "Step 3/10 — send the provider name, or - to skip.",
	"wizard.step.amount":           "Step 4/10 — send the amount like 18.00 USD, or - for no fixed amount.",
	"wizard.step.currency":         "Step 4/10 — select the currency for %s.",
	"wizard.step.billingDay":       "Step 5/10 — send the billing day (1-31).",
	"wizard.step.asset":            "Step 6/10 — select an asset to link, or Skip.",
	"wizard.step.reminderTime":     "Step 7/10 — send the reminder time HH:MM (default %s), or - to keep it.",
	"wizard.step.timezone":         "Step 8/10 — send the IANA timezone (default %s), or - to keep it.",
	"wizard.step.paymentURL":       "Step 9/10 — send an https payment URL, or - to skip.",
	"wizard.step.notes":            "Step 10/10 — send notes, or - to skip.",
	"wizard.step.review":           "Review the plan.",
	"wizard.button.monthly":        "Monthly (v1 only)",
	"wizard.button.skip":           "Skip",
	"wizard.button.back":           "Back",
	"wizard.button.cancel":         "Cancel",
	"wizard.button.confirm":        "Confirm",
	"wizard.planUpdated":           "Plan updated: %s (id=%s)",
	"wizard.planCreated":           "Plan created: %s (id=%s)",
	"wizard.notActive":             "Wizard is not active",
	"wizard.expiredShort":          "Wizard expired",
	"wizard.noLongerActive":        "This wizard is no longer active",
	"wizard.cancelledShort":        "Wizard cancelled",
	"wizard.backShort":             "Back",
	"wizard.useTextPrompt":         "Use the text prompt to continue",
	"wizard.planSaved":             "Plan saved",
	"wizard.onlyMonthly":           "Only monthly is supported",
	"wizard.selectedMonthly":       "Selected monthly",
	"wizard.unsupportedCurrency":   "Unsupported currency",
	"wizard.selectedCurrency":      "Selected %s",
	"wizard.assetSkipped":          "Asset skipped",
	"wizard.assetLinked":           "Asset linked",
	"wizard.unknownAsset":          "Unknown asset",
	"wizard.noValue":               "No value to select here",
}

var catalogZH = map[string]string{
	// common
	"common.unknown":       "未知",
	"common.uncategorized": "未分类",
	"common.enabled":       "已启用",
	"common.disabled":      "已停用",

	// errors
	"error.unauthorized":         "未授权",
	"error.unauthorizedSentence": "未授权。",
	"error.unknownAction":        "未知操作",
	"error.commandFailed":        "命令执行失败，请查看服务器日志。",
	"error.accountNotFound":      "未找到账号。",
	"error.invoiceNotFound":      "未找到账单",
	"error.mailNotFound":         "未找到邮件",
	"error.assetNotFound":        "未找到资产",
	"error.reminderNotFound":     "未找到提醒。",
	"error.folderNotFound":       "该账号下未找到此文件夹。",
	"error.invoiceConflict":      "当前状态下无法修改此账单",
	"error.reminderConflict":     "当前状态下无法修改此提醒。",
	"error.reminderInvalid":      "提醒信息无效。",
	"error.paymentUpdate":        "支付状态更新失败",
	"error.removalFailed":        "停用失败",
	"error.planLookup":           "计划查询失败",
	"error.historyLookup":        "历史查询失败",
	"error.reminderUpdate":       "提醒更新失败",
	"error.manualNotConfigured":  "手动提醒未配置。",
	"error.noInvoiceHistory":     "暂无账单历史",
	"error.invoiceNotPending":    "账单不在待确认状态",

	// start and help
	"start.welcome":       "Invoice Guard 已就绪。使用 /help 查看可用命令。",
	"help.unknownCommand": "未知命令，请使用 /help。",
	"help.body": `<b>Invoice Guard 命令</b>
/start - 欢迎信息
/help - 显示本帮助
/add microsoft|qq - 连接邮箱
/accounts - 列出已连接账号
/remove &lt;accountID&gt; [confirm] - 停用账号
/sync - 立即同步邮箱
/latest - 最近 5 封邮件
/search &lt;text&gt; - 搜索已存储邮件
/invoices [pending|paid|overdue|review] - 列出账单
/pending /paid /overdue - 账单快捷命令
/assets [provider] - 列出已跟踪资产
/providers - 支持的来源与解析器
/status - 系统统计
/settings - 查看或修改设置
/settings folder &lt;accountID&gt; - 列出账号的文件夹
/settings folder &lt;accountID&gt; &lt;folderID&gt; on|off - 开启或关闭文件夹同步`,
	"help.manualBody": `<b>手动周期提醒</b>
/reminder_add - 用向导创建周期账单
/reminders - 列出手动计划
/reminder &lt;id&gt; - 查看单个计划及其待确认周期
/reminder_edit &lt;id&gt; - 用向导修改计划
/reminder_pause &lt;id&gt; - 暂停计划
/reminder_resume &lt;id&gt; - 恢复已暂停的计划
/reminder_delete &lt;id&gt; - 取消计划
/reminder_paid &lt;occurrenceID&gt; - 确认某个周期已支付
/reminder_pending - 列出全部待确认周期
/reminder_history &lt;id&gt; - 计划事件与投递记录
/cancel - 取消当前向导`,

	// /add
	"add.usage":       "用法：/add microsoft|qq",
	"add.unsupported": "不支持的服务商，请使用 microsoft 或 qq。",
	"add.noLink":      "接入流程未返回链接，请查看服务器日志。",
	"add.openLink":    "打开以下一次性链接以连接 %s：\n%s",

	// accounts
	"accounts.empty":     "尚未连接任何账号，请使用 /add microsoft|qq。",
	"accounts.more":      "… 以及另外 %d 个",
	"accounts.line":      "• %s（%s，%s）id=%s",
	"accounts.syncError": " 同步错误=%s",
	"accounts.title":     "账号",

	// remove
	"remove.usage":         "用法：/remove <accountID> [confirm]",
	"remove.confirmPrompt": "停用账号 %s？历史邮件与账单将保留。",
	"remove.confirmButton": "确认停用",
	"remove.disabled":      "账号已停用：%s",
	"remove.disabledShort": "账号已停用",

	// sync
	"sync.notConfigured": "同步未配置。",
	"sync.running":       "同步已在运行。",
	"sync.started":       "同步已在后台开始。",

	// lists
	"list.empty":               "%s：未找到内容。",
	"list.mailLine":            "%d. %s — %s（%s）",
	"latest.title":             "最近邮件",
	"search.usage":             "用法：/search <text>",
	"search.title":             "搜索结果",
	"mail.button":              "邮件 %d",
	"invoices.title":           "账单",
	"invoices.pending":         "待确认账单",
	"invoices.paid":            "已确认账单",
	"invoices.review":          "需复核账单",
	"invoices.overdue":         "已逾期账单",
	"invoices.usage":           "用法：/invoices [pending|paid|overdue|review]",
	"invoices.line":            "%d. %s %s 到期 %s（%s）",
	"invoices.button":          "账单 %d",
	"assets.empty":             "资产：未找到内容。",
	"assets.title":             "资产",
	"assets.line":              "%d. %s（%s）",
	"assets.historyButton":     "历史 %d",
	"providers.sources":        "来源：microsoft、qq",
	"providers.parser":         "解析器：%s v%s",
	"providers.title":          "服务商",
	"status.accounts":          "账号：%d",
	"status.emails":            "邮件：%d",
	"status.pendingInvoices":   "待确认账单：%d",
	"status.pendingDeliveries": "待投递消息：%d",
	"status.title":             "系统状态",

	// invoice status labels
	"invoice.status.pending":   "PENDING_CONFIRMATION（需手动确认）",
	"invoice.status.paid":      "PAID_CONFIRMED（已手动确认）",
	"invoice.status.review":    "REVIEW_REQUIRED（需复核）",
	"invoice.status.cancelled": "CANCELLED（已取消）",

	// callbacks
	"callback.paidPrompt":        "确认已手动支付账单 %s（%s）？\n只有确实已支付时才按「确认」。",
	"callback.confirmPaid":       "确认已支付",
	"callback.undo":              "撤销",
	"callback.cancel":            "取消",
	"callback.confirmPayment":    "确认支付",
	"callback.alreadyPaid":       "已标记为已支付",
	"callback.markedPaid":        "已标记为已支付",
	"callback.alreadyPending":    "已是待确认状态",
	"callback.restoredPending":   "已恢复为待确认",
	"callback.invoiceCancelled":  "账单已取消",
	"callback.openingMail":       "正在打开邮件",
	"callback.openingInvoice":    "正在打开账单",
	"callback.assetHistory":      "资产历史",
	"callback.assetHistoryTitle": "资产账单历史",

	// email notification
	"email.title":          "<b>收到新邮件</b>",
	"email.account":        "账号：%s",
	"email.provider":       "服务商：%s",
	"email.from":           "发件人：%s",
	"email.vendor":         "厂商：%s / %s",
	"email.subject":        "主题：%s",
	"email.time":           "时间：%s",
	"email.noText":         "（无文本内容）",
	"email.links":          "<b>链接</b>",
	"email.attachments":    "<b>附件（%d）</b>",
	"email.bullet":         "• %s",
	"email.attachmentType": "（%s）",
	"email.attachmentSize": " %d 字节",

	// invoice notification
	"invoice.title":          "<b>账单</b>",
	"invoice.provider":       "服务商：%s",
	"invoice.account":        "账号：%s",
	"invoice.status":         "状态：%s",
	"invoice.amountDue":      "应付金额：%s",
	"invoice.subtotal":       "小计：%s",
	"invoice.credit":         "抵扣：%s",
	"invoice.total":          "合计：%s",
	"invoice.balance":        "账户余额：%s",
	"invoice.created":        "创建日期：%s",
	"invoice.due":            "到期日：%s",
	"invoice.paymentMethod":  "支付方式：%s",
	"invoice.confirmedAt":    "确认时间：%s",
	"invoice.item":           "<b>项目 %d</b>",
	"invoice.model":          "型号：%s",
	"invoice.host":           "主机：%s",
	"invoice.os":             "系统：%s",
	"invoice.period":         "周期：%s – %s",
	"invoice.originalPrice":  "原价：%s",
	"invoice.discount":       "优惠：%s",
	"invoice.finalPrice":     "最终价格：%s",
	"invoice.promo":          "优惠码：%s",
	"invoice.discountType":   "优惠方式：%s",
	"invoice.reviewRequired": "<b>需要复核</b>",
	"invoice.manualNote":     "支付状态为手动确认：只有授权用户才能将此账单标记为已支付。",
	"invoice.dateUnknown":    "？",

	// reminder note
	"reminder.unknownDue": "提醒：到期日未知。",
	"reminder.dueOn":      "提醒：到期日 %s。",
	"reminder.dueIn":      "提醒：还有 %d 天到期（%s）。",
	"reminder.dueToday":   "提醒：今天到期（%s）。",
	"reminder.overdue":    "提醒：已逾期 %d 天（%s）。",

	// settings
	"settings.usage":           "用法：/settings [timezone <tz> | reminder <HH:MM> | sync <秒> | folder <accountID> [<folderID> on|off] | language en|zh-CN]",
	"settings.title":           "设置",
	"settings.timezoneLine":    "时区：%s",
	"settings.reminderLine":    "提醒时间：%s",
	"settings.syncLine":        "同步间隔秒数：%d",
	"settings.folderLine":      "文件夹：/settings folder <accountID> [<folderID> on|off]",
	"settings.languageLine":    "语言：%s",
	"settings.timezoneUsage":   "用法：/settings timezone <IANA 名称>",
	"settings.timezoneInvalid": "时区无效，请使用 IANA 名称，例如 Asia/Taipei。",
	"settings.timezoneSet":     "时区已设为 %s。",
	"settings.reminderUsage":   "用法：/settings reminder <HH:MM>",
	"settings.reminderInvalid": "提醒时间无效，请使用 24 小时制 HH:MM。",
	"settings.reminderSet":     "提醒时间已设为 %s。",
	"settings.syncUsage":       "用法：/settings sync <秒>",
	"settings.syncInvalid":     "间隔无效，请使用正整数秒数（最大 86400）。",
	"settings.syncSet":         "同步间隔已设为 %d 秒。",
	"settings.folderUsage":     "用法：/settings folder <accountID> [<folderID> on|off]",
	"settings.folderEmpty":     "账号 %s 尚未存储文件夹。请先运行 /sync 发现文件夹。",
	"settings.foldersTitle":    "%s 的文件夹",
	"settings.folderItem":      "• %s id=%s（%s）",
	"settings.folderState":     "文件夹状态必须为 on 或 off。",
	"settings.folderSet":       "文件夹 %s（%s）现在为 %s。",
	"settings.languageUsage":   "用法：/settings language en|zh-CN",
	"settings.languageInvalid": "语言无效，请使用 en 或 zh-CN。",
	"settings.languageSet":     "语言已设为 %s。",
	"settings.languageCurrent": "当前语言：%s。",

	// auth buttons
	"button.viewInvoice":  "查看账单",
	"button.originalMail": "原始邮件",
	"button.markPaid":     "标记已支付",
	"button.undoPaid":     "撤销已支付",

	// manual commands
	"manual.usagePrefix":          "用法：%s",
	"manual.noPlans":              "手动提醒：暂无计划。使用 /reminder_add 创建。",
	"manual.planLine":             "%d. %s — %s，下次到期 %s（id=%s）",
	"manual.planButton":           "计划 %d",
	"manual.title":                "手动提醒",
	"manual.usage.reminder":       "/reminder <id>",
	"manual.usage.edit":           "/reminder_edit <id>",
	"manual.usage.pause":          "/reminder_pause <id>",
	"manual.usage.resume":         "/reminder_resume <id>",
	"manual.usage.delete":         "/reminder_delete <id>",
	"manual.usage.paid":           "/reminder_paid <occurrenceID>",
	"manual.usage.history":        "/reminder_history <id>",
	"manual.paused":               "计划已暂停：%s",
	"manual.resumed":              "计划已恢复：%s，下次到期 %s",
	"manual.cancelled":            "计划已取消：%s",
	"manual.occurrencePaid":       "到期日 %s 的周期已标记为已支付。",
	"manual.pendingNone":          "待确认手动周期：无。",
	"manual.pendingLine":          "%d. %s — 到期 %s，%s（id=%s）",
	"manual.markPaidButton":       "标记已支付 %s",
	"manual.undoButton":           "撤销 %s",
	"manual.pendingTitle":         "待确认手动周期",
	"manual.wizardCancelled":      "向导已取消。",
	"manual.noActiveWizard":       "没有进行中的向导。",
	"manual.historyButton":        "历史",
	"manual.viewPlanButton":       "查看计划",
	"manual.amountNotFixed":       "金额未定",
	"manual.planStatus.active":    "ACTIVE（进行中）",
	"manual.planStatus.paused":    "PAUSED（已暂停）",
	"manual.planStatus.cancelled": "CANCELLED（已取消）",
	"manual.occStatus.pending":    "PENDING（未支付）",
	"manual.occStatus.paid":       "PAID（已支付）",
	"manual.occStatus.cancelled":  "CANCELLED（已取消）",

	// manual callbacks
	"manual.occNotPending":  "该周期不在待确认状态",
	"manual.confirmPrompt":  "确认已手动支付 %s（到期日 %s，%s）？\n只有确实已支付该周期时才按「确认」。",
	"manual.openingPlan":    "正在打开计划",
	"manual.openingHistory": "正在打开历史",

	// manual formatting
	"manualFormat.planTitle":        "<b>手动提醒计划</b>",
	"manualFormat.provider":         "服务商：%s",
	"manualFormat.status":           "状态：%s",
	"manualFormat.amount":           "金额：%s",
	"manualFormat.billingDay":       "账期日：%d",
	"manualFormat.reminder":         "提醒：%s（%s）",
	"manualFormat.nextDue":          "下次到期：%s",
	"manualFormat.description":      "描述：%s",
	"manualFormat.notes":            "备注：%s",
	"manualFormat.paymentURL":       "支付链接：%s",
	"manualFormat.pendingNone":      "待确认周期：无。",
	"manualFormat.pendingHeader":    "<b>待确认周期（%d）</b>",
	"manualFormat.occurrenceLine":   "%d. 到期 %s — %s — %s（id=%s）",
	"manualFormat.manualNote":       "支付状态为手动确认：只有授权用户才能将某个周期标记为已支付。",
	"manualFormat.summaryTitle":     "<b>手动提醒</b>",
	"manualFormat.planStatus":       "计划状态：%s",
	"manualFormat.occurrenceHeader": "<b>周期 %d</b>",
	"manualFormat.due":              "到期日：%s",
	"manualFormat.historyTitle":     "<b>手动提醒历史</b>",
	"manualFormat.eventsNone":       "事件：无。",
	"manualFormat.eventsHeader":     "<b>事件（%d）</b>",
	"manualFormat.eventLine":        "• %s 于 %s",
	"manualFormat.eventOccurrence":  "（周期 %s）",
	"manualFormat.previewTitle":     "<b>确认手动提醒</b>",
	"manualFormat.title":            "标题：%s",
	"manualFormat.period":           "周期：%s",
	"manualFormat.asset":            "资产：%s",
	"manualFormat.assetNone":        "资产：无",
	"manualFormat.paymentURLNone":   "支付链接：无",
	"manualFormat.confirmToSave":    "确认以保存此计划。",

	// wizard
	"wizard.expired":               "向导已过期，请重新使用 /reminder_add 开始。",
	"wizard.err.title":             "标题必须为 1-200 个字符。",
	"wizard.err.period":            "请选择「月度」继续；v1 仅支持月度计划。",
	"wizard.err.provider":          "服务商名称最多 100 个字符。",
	"wizard.err.amount":            "请发送类似 18.00 USD 的金额，或发送 - 表示金额未定。",
	"wizard.err.amountInvalid":     "金额无效或币种不受支持。",
	"wizard.err.currency":          "币种不受支持，请选择按钮中的一项。",
	"wizard.err.billingDay":        "账期日必须是 1 到 31 之间的数字。",
	"wizard.err.asset":             "请选择资产或「跳过」。",
	"wizard.err.reminderTime":      "提醒时间必须为 24 小时制 HH:MM。",
	"wizard.err.timezone":          "时区无效，请使用 IANA 名称，例如 Asia/Taipei。",
	"wizard.err.paymentURL":        "支付链接必须是 https 链接，且不能包含凭据、非 443 端口或片段。",
	"wizard.err.notes":             "备注最多 2000 个字符。",
	"wizard.err.confirm":           "请按「确认」、「返回」或「取消」。",
	"wizard.err.sendAmountFirst":   "请先发送金额。",
	"wizard.err.amountForCurrency": "金额对 %s 无效。",
	"wizard.assetUnavailable":      "资产不可用；请按「跳过」。",
	"wizard.assetLoadError":        "无法加载资产；请按「跳过」。",
	"wizard.step.title":            "第 1/10 步 — 发送计划标题。",
	"wizard.step.period":           "第 2/10 步 — 选择账期。v1 仅支持月度计划。",
	"wizard.step.provider":         "第 3/10 步 — 发送服务商名称，或发送 - 跳过。",
	"wizard.step.amount":           "第 4/10 步 — 发送类似 18.00 USD 的金额，或发送 - 表示金额未定。",
	"wizard.step.currency":         "第 4/10 步 — 为 %s 选择币种。",
	"wizard.step.billingDay":       "第 5/10 步 — 发送账期日（1-31）。",
	"wizard.step.asset":            "第 6/10 步 — 选择要关联的资产，或「跳过」。",
	"wizard.step.reminderTime":     "第 7/10 步 — 发送提醒时间 HH:MM（默认 %s），或发送 - 保持不变。",
	"wizard.step.timezone":         "第 8/10 步 — 发送 IANA 时区（默认 %s），或发送 - 保持不变。",
	"wizard.step.paymentURL":       "第 9/10 步 — 发送 https 支付链接，或发送 - 跳过。",
	"wizard.step.notes":            "第 10/10 步 — 发送备注，或发送 - 跳过。",
	"wizard.step.review":           "请检查计划。",
	"wizard.button.monthly":        "月度（仅 v1）",
	"wizard.button.skip":           "跳过",
	"wizard.button.back":           "返回",
	"wizard.button.cancel":         "取消",
	"wizard.button.confirm":        "确认",
	"wizard.planUpdated":           "计划已更新：%s（id=%s）",
	"wizard.planCreated":           "计划已创建：%s（id=%s）",
	"wizard.notActive":             "向导未激活",
	"wizard.expiredShort":          "向导已过期",
	"wizard.noLongerActive":        "此向导已不再有效",
	"wizard.cancelledShort":        "向导已取消",
	"wizard.backShort":             "返回",
	"wizard.useTextPrompt":         "请使用文本提示继续",
	"wizard.planSaved":             "计划已保存",
	"wizard.onlyMonthly":           "仅支持月度",
	"wizard.selectedMonthly":       "已选择月度",
	"wizard.unsupportedCurrency":   "币种不受支持",
	"wizard.selectedCurrency":      "已选择 %s",
	"wizard.assetSkipped":          "已跳过资产",
	"wizard.assetLinked":           "已关联资产",
	"wizard.unknownAsset":          "未知资产",
	"wizard.noValue":               "此处没有可选择的数值",
}

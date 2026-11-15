package telegram

import (
	"context"
	"strings"
	"testing"

	"email-invoice-guard/internal/core"
)

// newLangEnv builds a test environment with an explicit display language. An
// empty language removes the setting so the zh-CN default applies.
func newLangEnv(t *testing.T, lg lang) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	env.store.mu.Lock()
	if lg == "" {
		delete(env.store.settings, settingLanguage)
	} else {
		env.store.settings[settingLanguage] = string(lg)
	}
	env.store.mu.Unlock()
	return env
}

func TestCatalogsHaveIdenticalKeysAndNoEmptyValues(t *testing.T) {
	en := catalogs[langEN]
	zh := catalogs[langZH]
	if en == nil || zh == nil {
		t.Fatal("missing catalog for en or zh-CN")
	}
	for key := range en {
		if _, ok := zh[key]; !ok {
			t.Errorf("zh-CN catalog missing key %q", key)
		}
	}
	for key := range zh {
		if _, ok := en[key]; !ok {
			t.Errorf("en catalog missing key %q", key)
		}
	}
	for lg, catalog := range catalogs {
		for key, value := range catalog {
			if strings.TrimSpace(value) == "" {
				t.Errorf("catalog %s has empty value for %q", lg, key)
			}
		}
	}
}

func TestDefaultLanguageIsSimplifiedChinese(t *testing.T) {
	env := newLangEnv(t, "")
	if got := env.bot.language(context.Background()); got != langZH {
		t.Fatalf("default language = %q, want %q", got, langZH)
	}
	text := sentText(env.runMessage("/start"))
	if !strings.Contains(text, "已就绪") {
		t.Fatalf("default start reply is not Chinese: %q", text)
	}
	if strings.Contains(text, "Invoice Guard is ready") {
		t.Fatalf("English leaked into the default Chinese path: %q", text)
	}
}

func TestInvalidStoredLanguageFallsBackToChinese(t *testing.T) {
	env := newLangEnv(t, "")
	env.store.mu.Lock()
	env.store.settings[settingLanguage] = "fr"
	env.store.mu.Unlock()
	if got := env.bot.language(context.Background()); got != langZH {
		t.Fatalf("invalid stored language resolved to %q, want %q", got, langZH)
	}
	if !strings.Contains(env.logs.String(), "invalid language setting") {
		t.Fatal("invalid stored language was not logged")
	}
}

func TestSettingsLanguagePersistsAndSwitches(t *testing.T) {
	env := newLangEnv(t, "")
	ctx := context.Background()

	// No argument shows the current (default) language.
	text := sentText(env.runMessage("/settings language"))
	if !strings.Contains(text, "zh-CN") {
		t.Fatalf("current language reply = %q", text)
	}

	// en switches immediately and persists.
	env.runMessage("/settings language en")
	if v, _ := env.store.setting(settingLanguage); v != "en" {
		t.Fatalf("language not persisted: %q", v)
	}
	if got := env.bot.language(ctx); got != langEN {
		t.Fatalf("language did not switch: %q", got)
	}
	if text := sentText(env.runMessage("/start")); !strings.Contains(text, "Invoice Guard is ready") {
		t.Fatalf("English switch did not apply: %q", text)
	}

	// Aliases normalize to zh-CN.
	for _, alias := range []string{"zh", "zh-cn", "cn", "ZH-CN"} {
		env.runMessage("/settings language " + alias)
		if v, _ := env.store.setting(settingLanguage); v != string(langZH) {
			t.Fatalf("alias %q persisted %q, want %q", alias, v, langZH)
		}
	}

	// An invalid value is rejected and leaves the stored value unchanged.
	env.runMessage("/settings language en")
	env.tg.reset()
	text = sentText(env.runMessage("/settings language fr"))
	if !strings.Contains(text, "Invalid language") {
		t.Fatalf("invalid language reply = %q", text)
	}
	if v, _ := env.store.setting(settingLanguage); v != "en" {
		t.Fatalf("invalid language overwrote the setting: %q", v)
	}
}

func TestStatusCommandBothLanguages(t *testing.T) {
	status := core.SystemStatus{Accounts: 2, Emails: 30, PendingInvoices: 4, PendingDeliveries: 1}
	cases := []struct {
		lang    lang
		want    []string
		leakage string
	}{
		{langEN, []string{"Accounts: 2", "Emails: 30", "Pending invoices: 4", "Pending deliveries: 1"}, ""},
		{langZH, []string{"账号：2", "邮件：30", "待确认账单：4", "待投递消息：1"}, "Accounts:"},
	}
	for _, tc := range cases {
		env := newLangEnv(t, tc.lang)
		env.store.status = func(context.Context) (core.SystemStatus, error) { return status, nil }
		text := sentText(env.runMessage("/status"))
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Fatalf("%s status missing %q: %q", tc.lang, want, text)
			}
		}
		if tc.leakage != "" && strings.Contains(text, tc.leakage) {
			t.Fatalf("English leaked into %s status: %q", tc.lang, text)
		}
	}
}

func TestPaidPromptBothLanguages(t *testing.T) {
	cases := []struct {
		lang    lang
		want    string
		leakage string
	}{
		{langEN, "Confirm manual payment for invoice", ""},
		{langZH, "确认已手动支付账单", "Confirm manual payment"},
	}
	for _, tc := range cases {
		env := newLangEnv(t, tc.lang)
		invoice := sampleInvoice()
		env.store.getInvoice = func(context.Context, string) (core.Invoice, error) { return invoice, nil }
		env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
		env.runCallback(callbackData(cbPaidPrompt, invoice.ID))
		text := editedText(env.tg)
		if !strings.Contains(text, tc.want) {
			t.Fatalf("%s paid prompt missing %q: %q", tc.lang, tc.want, text)
		}
		if tc.leakage != "" && strings.Contains(text, tc.leakage) {
			t.Fatalf("English leaked into %s paid prompt: %q", tc.lang, text)
		}
	}
}

func TestEmailNotificationBothLanguages(t *testing.T) {
	cases := []struct {
		lang    lang
		header  string
		leakage string
	}{
		{langEN, "New mail received", ""},
		{langZH, "收到新邮件", "New mail received"},
	}
	for _, tc := range cases {
		env := newLangEnv(t, tc.lang)
		email := sampleEmail()
		env.store.getEmail = func(context.Context, string) (core.NormalizedEmail, error) { return email, nil }
		env.store.getAccount = func(context.Context, string) (core.MailAccount, error) { return sampleAccount(), nil }
		env.runCallback(callbackData(cbEmail, email.InternalEmailID))
		text := sentText(env.tg.callsFor("sendMessage"))
		if !strings.Contains(text, tc.header) {
			t.Fatalf("%s email notification missing %q: %q", tc.lang, tc.header, text)
		}
		if tc.leakage != "" && strings.Contains(text, tc.leakage) {
			t.Fatalf("English leaked into %s email notification: %q", tc.lang, text)
		}
	}
}

func TestInvoiceNotificationBothLanguages(t *testing.T) {
	cases := []struct {
		lang    lang
		header  string
		amount  string
		leakage string
	}{
		{langEN, "Invoice", "Amount due:", ""},
		{langZH, "账单", "应付金额：", "Amount due:"},
	}
	for _, tc := range cases {
		env := newLangEnv(t, tc.lang)
		rec := &deliveryRecorder{}
		rec.install(env.store)
		invoice := sampleInvoice()
		env.invoiceDelivery(invoice, core.DeliveryPayload{Kind: "invoice", EmailID: testID(1)})
		if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		text := sentText(env.tg.callsFor("sendMessage"))
		if !strings.Contains(text, tc.header) {
			t.Fatalf("%s invoice notification missing %q: %q", tc.lang, tc.header, text)
		}
		if !strings.Contains(text, tc.amount) {
			t.Fatalf("%s invoice notification missing %q: %q", tc.lang, tc.amount, text)
		}
		if tc.leakage != "" && strings.Contains(text, tc.leakage) {
			t.Fatalf("English leaked into %s invoice notification: %q", tc.lang, text)
		}
	}
}

func TestManualSummaryBothLanguages(t *testing.T) {
	cases := []struct {
		lang    lang
		header  string
		leakage string
	}{
		{langEN, "Manual reminder", ""},
		{langZH, "手动提醒", "Manual reminder"},
	}
	for _, tc := range cases {
		env := newLangEnv(t, tc.lang)
		rec := &deliveryRecorder{}
		rec.install(env.store)
		plan := sampleManualPlan()
		occ := sampleManualOccurrence(core.ManualOccurrencePending)
		env.manualDelivery(occurrenceService(plan, []core.ManualOccurrence{occ}), plan, []core.ManualOccurrence{occ})
		if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		text := sentText(env.tg.callsFor("sendMessage"))
		if !strings.Contains(text, tc.header) {
			t.Fatalf("%s manual summary missing %q: %q", tc.lang, tc.header, text)
		}
		if !strings.Contains(text, "VPS renewal") {
			t.Fatalf("%s manual summary lost the plan title: %q", tc.lang, text)
		}
		if tc.leakage != "" && strings.Contains(text, tc.leakage) {
			t.Fatalf("English leaked into %s manual summary: %q", tc.lang, text)
		}
	}
}

func TestEscapingBothLanguages(t *testing.T) {
	for _, lg := range []lang{langEN, langZH} {
		env := newLangEnv(t, lg)
		rec := &deliveryRecorder{}
		rec.install(env.store)
		plan := sampleManualPlan()
		plan.Title = `<b>&`
		occ := sampleManualOccurrence(core.ManualOccurrencePending)
		env.manualDelivery(occurrenceService(plan, []core.ManualOccurrence{occ}), plan, []core.ManualOccurrence{occ})
		if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		text := sentText(env.tg.callsFor("sendMessage"))
		if !strings.Contains(text, "&lt;b&gt;&amp;") {
			t.Fatalf("%s did not escape the title: %q", lg, text)
		}
		if strings.Contains(text, "<b>&") {
			t.Fatalf("%s left the raw title unescaped: %q", lg, text)
		}
	}
}

func TestLongMessagesStayWithinLimitBothLanguages(t *testing.T) {
	for _, lg := range []lang{langEN, langZH} {
		t.Run(string(lg)+"/email", func(t *testing.T) {
			env := newLangEnv(t, lg)
			rec := &deliveryRecorder{}
			rec.install(env.store)
			email := sampleEmail()
			email.TextBody = strings.Repeat("body line\n", 900)
			env.emailDelivery(email)
			if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertPartsWithinLimit(t, env.tg.callsFor("sendMessage"))
		})
		t.Run(string(lg)+"/manual", func(t *testing.T) {
			env := newLangEnv(t, lg)
			rec := &deliveryRecorder{}
			rec.install(env.store)
			plan := sampleManualPlan()
			plan.Title = strings.Repeat("あ", 5000)
			occ := sampleManualOccurrence(core.ManualOccurrencePending)
			env.manualDelivery(occurrenceService(plan, []core.ManualOccurrence{occ}), plan, []core.ManualOccurrence{occ})
			if _, err := env.bot.DeliverOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertPartsWithinLimit(t, env.tg.callsFor("sendMessage"))
		})
	}
}

func assertPartsWithinLimit(t *testing.T, calls []tgCall) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no message parts were sent")
	}
	for i, call := range calls {
		text, _ := call.Body["text"].(string)
		if units := utf16Len(text); units > maxMessageUnits {
			t.Fatalf("part %d has %d UTF-16 units, want <= %d", i, units, maxMessageUnits)
		}
	}
}

func TestManualCallbackDataIdenticalAcrossLanguages(t *testing.T) {
	collect := func(lg lang) []string {
		env := newLangEnv(t, lg)
		env.withManual(manualPlanService())
		env.runCallback(callbackData(cbManualPaid, testID(21)))
		call, ok := env.tg.last("editMessageText")
		if !ok {
			t.Fatalf("%s: no confirmation prompt was edited", lg)
		}
		return callbackDataValues(call.Body["reply_markup"])
	}
	en := collect(langEN)
	zh := collect(langZH)
	if len(en) == 0 || len(zh) == 0 {
		t.Fatalf("missing callback data: en=%v zh=%v", en, zh)
	}
	if strings.Join(en, ",") != strings.Join(zh, ",") {
		t.Fatalf("callback data changed with language: en=%v zh=%v", en, zh)
	}
}

func TestExportedFormattersStayEnglish(t *testing.T) {
	email := sampleEmail()
	if text := joined(FormatEmail(email, sampleAccount())); !strings.Contains(text, "New mail received") {
		t.Fatalf("FormatEmail is not English: %q", text)
	}
	invoice := sampleInvoice()
	if text := joined(FormatInvoice(invoice, sampleAccount())); !strings.Contains(text, "Amount due:") {
		t.Fatalf("FormatInvoice is not English: %q", text)
	}
}

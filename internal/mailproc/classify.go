package mailproc

import (
	"strings"

	"email-invoice-guard/internal/core"
)

// Classification categories. Classification is descriptive only: it never
// filters or suppresses a mail notification, and forwarded mail is classified
// by its content like any other message.
const (
	CategoryInvoice     = "invoice"
	CategorySecurity    = "security"
	CategoryMaintenance = "maintenance"
	CategoryPromotion   = "promotion"
	CategoryOther       = "other"
)

type categoryRule struct {
	name     string
	keywords []string
}

// Rules are evaluated in priority order; ties keep the earlier category.
var classificationRules = []categoryRule{
	{CategoryInvoice, []string{
		"invoice", "tax invoice", "receipt", "billing", "bill", "amount due",
		"payment due", "payment received", "order confirmation", "order #",
		"statement", "renewal", "發票", "账单", "帳單", "付款", "收據", "收据",
	}},
	{CategorySecurity, []string{
		"security alert", "security notice", "sign-in", "sign in", "new login",
		"login attempt", "password", "two-factor", "2fa", "verification code",
		"suspicious", "unauthorized access", "安全", "登入", "登录", "驗證", "验证",
	}},
	{CategoryMaintenance, []string{
		"maintenance", "scheduled maintenance", "downtime", "outage",
		"service notice", "upgrade", "維護", "维护", "停機", "停机", "升級", "升级",
	}},
	{CategoryPromotion, []string{
		"sale", "discount", "promo", "coupon", "offer", "deal", "newsletter",
		"unsubscribe", "limited time", "優惠", "优惠", "促銷", "促销", "限時", "限时",
	}},
}

// Classify assigns one of invoice/security/maintenance/promotion/other.
// Forwarding prefixes are ignored for matching but never used to filter mail.
func Classify(email core.NormalizedEmail) core.Classification {
	subject := strings.ToLower(stripForwardPrefix(email.Subject))
	body := strings.ToLower(email.TextBody)
	if strings.TrimSpace(body) == "" {
		body = strings.ToLower(HTMLToText(email.HTMLBody))
	}

	best := CategoryOther
	bestScore := 0
	for _, rule := range classificationRules {
		score := 0
		for _, keyword := range rule.keywords {
			score += 3 * strings.Count(subject, keyword)
			score += strings.Count(body, keyword)
		}
		if score > bestScore {
			best = rule.name
			bestScore = score
		}
	}
	return core.Classification{Category: best}
}

func stripForwardPrefix(subject string) string {
	s := strings.TrimSpace(subject)
	for {
		lower := strings.ToLower(s)
		switch {
		case strings.HasPrefix(lower, "fwd:"):
			s = strings.TrimSpace(s[4:])
		case strings.HasPrefix(lower, "fw:"):
			s = strings.TrimSpace(s[3:])
		case strings.HasPrefix(lower, "re:"):
			s = strings.TrimSpace(s[3:])
		default:
			return s
		}
	}
}

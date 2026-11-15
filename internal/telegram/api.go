package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL    = "https://api.telegram.org"
	maxResponseBytes  = 4 << 20
	redactedTokenText = "[redacted]"
)

// update is the subset of a Telegram update this bot consumes.
type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

type message struct {
	MessageID int64  `json:"message_id"`
	From      *user  `json:"from"`
	Chat      chat   `json:"chat"`
	Text      string `json:"text"`
	Date      int64  `json:"date"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type user struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// apiError is a definitive Telegram API rejection. It is a known failure: the
// request reached Telegram and was refused.
type apiError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *apiError) Error() string {
	desc := e.Description
	if len(desc) > 200 {
		desc = desc[:200]
	}
	return fmt.Sprintf("telegram: %s rejected (code %d): %s", e.Method, e.Code, desc)
}

// transportError is an ambiguous failure: the request may or may not have been
// delivered. Its message is already redacted by the client.
type transportError struct {
	msg     string
	timeout bool
}

func (e *transportError) Error() string { return "telegram: transport failure: " + e.msg }
func (e *transportError) Timeout() bool { return e.timeout }

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func (c *apiClient) url(method string) string {
	return c.baseURL + "/bot" + c.token + "/" + method
}

func (c *apiClient) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, redactedTokenText)
}

func (c *apiClient) call(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: encode %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: build %s request: %s", method, c.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return &transportError{msg: c.redact(err.Error()), timeout: isTimeout(err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return &transportError{msg: c.redact(err.Error()), timeout: isTimeout(err)}
	}
	var env apiResponse
	if err := json.Unmarshal(data, &env); err != nil {
		// The request reached Telegram but the answer is unreadable. For a
		// send this is ambiguous, so it must not be recorded as a definite
		// failure.
		return &transportError{msg: "invalid response body"}
	}
	if !env.OK || resp.StatusCode != http.StatusOK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		ae := &apiError{Method: method, Code: code, Description: c.redact(env.Description)}
		if env.Parameters != nil && env.Parameters.RetryAfter > 0 {
			ae.RetryAfter = time.Duration(env.Parameters.RetryAfter) * time.Second
		}
		return ae
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return &transportError{msg: "invalid result body"}
		}
	}
	return nil
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func (c *apiClient) getUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]update, error) {
	seconds := int(timeout / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	payload := map[string]any{
		"offset":          offset,
		"timeout":         seconds,
		"allowed_updates": []string{"message", "callback_query"},
	}
	var updates []update
	if err := c.call(ctx, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

type sendMessageRequest struct {
	ChatID                int64           `json:"chat_id"`
	Text                  string          `json:"text"`
	ParseMode             string          `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool            `json:"disable_web_page_preview,omitempty"`
	ReplyMarkup           *inlineKeyboard `json:"reply_markup,omitempty"`
}

func (c *apiClient) sendMessage(ctx context.Context, chatID int64, text string, markup *inlineKeyboard) (message, error) {
	req := sendMessageRequest{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	}
	var msg message
	if err := c.call(ctx, "sendMessage", req, &msg); err != nil {
		return message{}, err
	}
	if msg.MessageID == 0 {
		// A 200 response without a message id cannot be trusted as a
		// confirmed send; treat it as an ambiguous delivery.
		return message{}, &transportError{msg: "sendMessage returned no message id"}
	}
	return msg, nil
}

type editMessageRequest struct {
	ChatID                int64           `json:"chat_id"`
	MessageID             int64           `json:"message_id"`
	Text                  string          `json:"text"`
	ParseMode             string          `json:"parse_mode,omitempty"`
	DisableWebPagePreview bool            `json:"disable_web_page_preview,omitempty"`
	ReplyMarkup           *inlineKeyboard `json:"reply_markup,omitempty"`
}

func (c *apiClient) editMessageText(ctx context.Context, chatID, messageID int64, text string, markup *inlineKeyboard) error {
	req := editMessageRequest{
		ChatID:                chatID,
		MessageID:             messageID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	}
	return c.call(ctx, "editMessageText", req, nil)
}

type answerCallbackRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
}

func (c *apiClient) answerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	return c.call(ctx, "answerCallbackQuery", answerCallbackRequest{CallbackQueryID: id, Text: text, ShowAlert: alert}, nil)
}

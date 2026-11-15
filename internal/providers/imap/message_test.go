package imap

import (
	"encoding/base64"
	"strings"
	"testing"
)

func validBase64Part(body string) []byte {
	return []byte("Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(body)))
}

func TestDecodeTextValidBase64(t *testing.T) {
	text, reason := decodeText(validBase64Part("你好，发票"))
	if reason != "" {
		t.Fatalf("reason = %q, want none", reason)
	}
	if text != "你好，发票" {
		t.Fatalf("text = %q", text)
	}
}

func TestDecodeTextCorruptedBase64PreservesRaw(t *testing.T) {
	raw := []byte("Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"!!!not-base64!!!")
	text, reason := decodeText(raw)
	if reason == "" {
		t.Fatal("corrupted base64 was not marked as a decode failure")
	}
	if text == "" {
		t.Fatal("corrupted base64 produced empty text")
	}
	if !strings.Contains(text, "!!!not-base64!!!") {
		t.Fatalf("raw encoded text was not preserved: %q", text)
	}
}

func TestDecodeTextCorruptedQuotedPrintablePreservesRaw(t *testing.T) {
	raw := []byte("Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Hello =ZZ world")
	text, _ := decodeText(raw)
	// Go's quoted-printable reader passes invalid escapes through; the
	// invariant is that the content is never lost.
	if !strings.Contains(text, "=ZZ") {
		t.Fatalf("raw quoted-printable text was not preserved: %q", text)
	}
}

func TestDecodeTextPartialReadPreservesDecodedAndRaw(t *testing.T) {
	raw := []byte("Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte("hello ")) + "!!!!")
	text, reason := decodeText(raw)
	if reason == "" {
		t.Fatal("partial base64 read was not marked as a decode failure")
	}
	if !strings.Contains(text, "hello") {
		t.Fatalf("partial decoded prefix was lost: %q", text)
	}
	if !strings.Contains(text, "!!!!") {
		t.Fatalf("remaining raw text was lost: %q", text)
	}
}

func TestDecodeTextHeaderlessBodyIsPreserved(t *testing.T) {
	raw := []byte("Hello plain text without MIME headers")
	text, reason := decodeText(raw)
	if text != string(raw) {
		t.Fatalf("headerless body = %q, want %q", text, raw)
	}
	if reason == "" {
		t.Fatal("headerless body was not marked as a decode fallback")
	}
}

func TestDecodeTextEmpty(t *testing.T) {
	text, reason := decodeText(nil)
	if text != "" || reason != "" {
		t.Fatalf("decodeText(nil) = %q, %q", text, reason)
	}
}

func TestSynthesizeMIMEPartDecodesBody(t *testing.T) {
	headers := synthesizeMIMEPart("text/plain", "utf-8", "base64")
	raw := joinMIMEPart(headers, []byte(base64.StdEncoding.EncodeToString([]byte("账单"))))
	text, reason := decodeText(raw)
	if reason != "" {
		t.Fatalf("reason = %q, want none", reason)
	}
	if text != "账单" {
		t.Fatalf("text = %q", text)
	}
}

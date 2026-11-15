package imap

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"

	"email-invoice-guard/internal/core"
)

type textSection struct {
	section   *imapv2.FetchItemBodySection
	mime      *imapv2.FetchItemBodySection
	mediaType string
	charset   string
	encoding  string
}

type attachmentPlan struct {
	index    int
	filename string
	media    string
	size     int64
}

// planMessage decides which text parts to download and which parts are
// attachment metadata only. Binary bodies are never fetched. A missing
// BODYSTRUCTURE is an explicit error: falling back to BODY[] would download
// attachments in full.
func planMessage(structure imapv2.BodyStructure) ([]textSection, []attachmentPlan, error) {
	var texts []textSection
	var attachments []attachmentPlan
	if structure == nil {
		return nil, nil, errors.New("imap: server did not return BODYSTRUCTURE")
	}
	switch root := structure.(type) {
	case *imapv2.BodyStructureSinglePart:
		media := root.MediaType()
		if isTextMedia(media) && !isAttachmentPart(root) {
			// BODY[] of a single-part message includes the message headers, so
			// the charset and transfer encoding are available to the decoder.
			texts = append(texts, textSection{
				section:   &imapv2.FetchItemBodySection{Peek: true},
				mediaType: media,
				charset:   root.Params["charset"],
				encoding:  root.Encoding,
			})
		} else {
			attachments = append(attachments, attachmentPlan{
				index:    1,
				filename: root.Filename(),
				media:    media,
				size:     int64(root.Size),
			})
		}
	case *imapv2.BodyStructureMultiPart:
		index := 0
		root.Walk(func(path []int, part imapv2.BodyStructure) bool {
			single, ok := part.(*imapv2.BodyStructureSinglePart)
			if !ok || len(path) == 0 {
				return true
			}
			index++
			media := single.MediaType()
			if isTextMedia(media) && !isAttachmentPart(single) {
				pathCopy := append([]int(nil), path...)
				texts = append(texts, textSection{
					section:   &imapv2.FetchItemBodySection{Part: pathCopy, Peek: true},
					mime:      &imapv2.FetchItemBodySection{Part: append([]int(nil), path...), Specifier: imapv2.PartSpecifierMIME, Peek: true},
					mediaType: media,
					charset:   single.Params["charset"],
					encoding:  single.Encoding,
				})
				return true
			}
			attachments = append(attachments, attachmentPlan{
				index:    index,
				filename: single.Filename(),
				media:    media,
				size:     int64(single.Size),
			})
			return true
		})
	}
	return texts, attachments, nil
}

func isTextMedia(media string) bool {
	switch strings.ToLower(media) {
	case "text/plain", "text/html":
		return true
	}
	return false
}

func isAttachmentPart(part *imapv2.BodyStructureSinglePart) bool {
	if disposition := part.Disposition(); disposition != nil && strings.EqualFold(disposition.Value, "attachment") {
		return true
	}
	return part.Filename() != ""
}

// decodeErrorHeader marks a text part whose MIME decoding failed. The body is
// still preserved via a safe raw fallback; the marker is a bounded diagnostic,
// never message content.
const decodeErrorHeader = "X-Invoice-Guard-Decode-Error"

// decodeText decodes transfer encoding and charset to UTF-8. Text parts are
// read in full: silently truncating or emptying a body would lose invoice
// content. On a decode/read failure it returns the original encoded text (and
// any partial decoded bytes) together with a bounded reason. Unknown charsets
// degrade to the raw decoded bytes rather than failing the whole sync.
func decodeText(raw []byte) (string, string) {
	if len(raw) == 0 {
		return "", ""
	}
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return string(raw), "mime-decode-failed"
	}
	if entity == nil {
		return string(raw), "mime-decode-failed"
	}
	body, readErr := io.ReadAll(entity.Body)
	if readErr != nil {
		decoded := string(body)
		if decoded == "" {
			return string(raw), "mime-read-failed"
		}
		// Preserve both the partial decoded prefix and the remaining raw
		// encoded text; never drop content on a read error.
		return decoded + "\n" + string(raw), "mime-read-failed"
	}
	if len(body) == 0 && len(raw) > 0 &&
		!bytes.Contains(raw, []byte("\r\n\r\n")) && !bytes.Contains(raw, []byte("\n\n")) {
		// The entire input was consumed as headers with no separator, which
		// means it was a headerless body. Preserve it instead of returning "".
		return string(raw), "mime-decode-empty"
	}
	return string(body), ""
}

// joinMIMEPart prepends a part's MIME headers to its body so the go-message
// decoder sees Content-Type charset and Content-Transfer-Encoding.
func joinMIMEPart(mime, body []byte) []byte {
	if len(mime) == 0 {
		return body
	}
	// BODY[<part>] carries only the part body; BODY[<part>.MIME] carries the
	// headers. Always prepend them: a text body may legitimately begin with a
	// "Content-Type:" line, so body content is never used to infer headers.
	combined := make([]byte, 0, len(mime)+4+len(body))
	combined = append(combined, mime...)
	switch {
	case bytes.HasSuffix(mime, []byte("\r\n\r\n")), bytes.HasSuffix(mime, []byte("\n\n")):
		// Header already terminates with a blank line.
	case bytes.HasSuffix(mime, []byte("\r\n")):
		combined = append(combined, '\r', '\n')
	case bytes.HasSuffix(mime, []byte("\n")):
		combined = append(combined, '\n')
	default:
		combined = append(combined, '\r', '\n', '\r', '\n')
	}
	combined = append(combined, body...)
	return combined
}

// synthesizeMIMEPart builds minimal MIME headers from BODYSTRUCTURE metadata
// when the server does not return BODY[<part>.MIME]. This keeps charset and
// transfer encoding available to the decoder instead of guessing from content.
func synthesizeMIMEPart(mediaType, charset, encoding string) []byte {
	var builder strings.Builder
	builder.WriteString("Content-Type: ")
	builder.WriteString(mediaType)
	if charset != "" {
		builder.WriteString("; charset=")
		builder.WriteString(charset)
	}
	builder.WriteString("\r\n")
	if encoding == "" {
		encoding = "7bit"
	}
	builder.WriteString("Content-Transfer-Encoding: ")
	builder.WriteString(encoding)
	builder.WriteString("\r\n\r\n")
	return []byte(builder.String())
}

func parseHeaders(raw []byte) map[string][]string {
	if len(raw) == 0 {
		return nil
	}
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	header, err := reader.ReadMIMEHeader()
	if err != nil && len(header) == 0 {
		return nil
	}
	out := make(map[string][]string, len(header))
	for key, values := range header {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func headerValue(headers map[string][]string, key string) string {
	if headers == nil {
		return ""
	}
	values := headers[textproto.CanonicalMIMEHeaderKey(key)]
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func toAddresses(list []imapv2.Address) []core.Address {
	out := make([]core.Address, 0, len(list))
	for _, item := range list {
		if item.IsGroupStart() || item.IsGroupEnd() {
			continue
		}
		out = append(out, core.Address{Name: item.Name, Address: item.Addr()})
	}
	return out
}

func mailAddressList(raw string) ([]core.Address, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, err := mail.ParseAddressList(raw)
	if err != nil {
		return nil, err
	}
	out := make([]core.Address, 0, len(parsed))
	for _, item := range parsed {
		out = append(out, core.Address{Name: item.Name, Address: item.Address})
	}
	return out, nil
}

func firstAddress(list []imapv2.Address) core.Address {
	addresses := toAddresses(list)
	if len(addresses) == 0 {
		return core.Address{}
	}
	return addresses[0]
}

// fingerprint builds the conservative cross-folder identity. Message-ID alone
// is insufficient, so sender, subject, sender date and decoded bodies are
// included.
func fingerprint(from core.Address, subject string, sentAt time.Time, internetMessageID, textBody, htmlBody string) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		strings.ToLower(strings.TrimSpace(from.Address)),
		subject,
		sentAt.UTC().Format(time.RFC3339Nano),
		internetMessageID,
		textBody,
		htmlBody,
	)
	return hex.EncodeToString(hash.Sum(nil))
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for i, part := range parts {
		if i > 0 {
			hash.Write([]byte{0})
		}
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

// ensure the charset package is linked so non-UTF-8 MIME parts are decoded.
var _ = charset.Reader

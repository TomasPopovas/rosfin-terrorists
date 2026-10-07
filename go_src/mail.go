package main

// Почтовые уведомления по итогам прогона (любой SMTP-сервер с STARTTLS,
// например Synology MailPlus на том же NAS), а также сохранение
// текстов и вложений сообщений лент.
//
// Пароль берётся из ROSFIN_SMTP_PASS (файл .env) и в коде/compose не хранится.

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Статусы раздела за прогон.
const (
	StatusUpdated   = "updated"
	StatusUnchanged = "unchanged"
	StatusError     = "error"
)

// ItemResult — итог по одному разделу.
type ItemResult struct {
	Section string
	Title   string
	Status  string
	Saved   SavedFile
	Files   []string
	Note    string
	// Link — прямая ссылка на раздел кабинета. Заполняется там, где
	// содержимое нужно открыть руками.
	Link string
	Err  error
}

func countStatuses(results []ItemResult) (updated, unchanged, failed int) {
	for _, r := range results {
		switch r.Status {
		case StatusUpdated:
			updated++
		case StatusUnchanged:
			unchanged++
		case StatusError:
			failed++
		}
	}
	return
}

func sendMail(cfg Config, subject, body string) error {
	if !cfg.MailEnabled {
		return nil
	}
	if cfg.SMTPPass == "" {
		return fmt.Errorf("ROSFIN_SMTP_PASS не задан, письмо не отправлено")
	}

	// Подключаемся по SMTPAddr (например, внутренний IP почтового сервера),
	// а имя SMTPHost используем для TLS и HELO. Разделение нужно, когда
	// публичное имя сервера резолвится во внешний адрес, где порт закрыт,
	// а сертификат выписан на имя, а не на IP.
	dialHost := cfg.SMTPAddr
	if dialHost == "" {
		dialHost = cfg.SMTPHost
	}
	addr := fmt.Sprintf("%s:%d", dialHost, cfg.SMTPPort)
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.SMTPHost}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
	if ok, _ := client.Extension("AUTH"); ok {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(cfg.SMTPUser); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(cfg.MailTo); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(buildMessage(cfg.SMTPUser, cfg.MailTo, subject, body))); err != nil {
		w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return client.Quit()
}

func buildMessage(from, to, subject, body string) string {
	// Заголовки — только ASCII: тема кодируется по RFC 2047 (=?utf-8?b?...?=),
	// тело — base64. Иначе сторонние клиенты (eM Client, Outlook) читают
	// «сырой» UTF-8 как Latin-1 и показывают кракозябры.
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + mime.BEncoding.Encode("utf-8", subject) + "\r\n")
	sb.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		sb.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	sb.WriteString(enc + "\r\n")
	return sb.String()
}

// summarizeRun формирует текст письма: что обновилось, что нет, где ошибка.
func summarizeRun(dir string, results []ItemResult) string {
	var sb strings.Builder

	if dir == "" {
		sb.WriteString("Проверка выполнена, скачивание не требовалось.\r\n\r\n")
	} else {
		sb.WriteString("Папка выгрузки: " + dir + "\r\n\r\n")
	}

	var updated, unchanged, failed []ItemResult
	for _, r := range results {
		switch r.Status {
		case StatusUpdated:
			updated = append(updated, r)
		case StatusUnchanged:
			unchanged = append(unchanged, r)
		case StatusError:
			failed = append(failed, r)
		}
	}

	if len(updated) > 0 {
		sb.WriteString("ИЗМЕНИЛОСЬ:\r\n")
		for _, r := range updated {
			sb.WriteString(fmt.Sprintf("  [%s] %s\r\n", r.Section, r.Title))
			if r.Note != "" {
				sb.WriteString("    " + r.Note + "\r\n")
			}
			if r.Saved.Archive != "" {
				sb.WriteString(fmt.Sprintf("    %s (%.1f МБ, sha256 %s...)\r\n",
					r.Saved.Archive, float64(r.Saved.Bytes)/(1<<20), shortHash(r.Saved.SHA256)))
				if len(r.Saved.Extracted) > 0 {
					sb.WriteString("    распаковано: " + strings.Join(r.Saved.Extracted, ", ") + "\r\n")
				}
			}
			for _, f := range r.Files {
				sb.WriteString("    " + f + "\r\n")
			}
			if r.Link != "" {
				sb.WriteString("    открыть в кабинете: " + r.Link + "\r\n")
			}
		}
		sb.WriteString("\r\n")
	}

	if len(unchanged) > 0 {
		sb.WriteString("БЕЗ ИЗМЕНЕНИЙ (не скачивалось):\r\n")
		for _, r := range unchanged {
			sb.WriteString(fmt.Sprintf("  [%s] %s\r\n", r.Section, r.Title))
		}
		sb.WriteString("\r\n")
	}

	if len(failed) > 0 {
		sb.WriteString("ОШИБКИ:\r\n")
		for _, r := range failed {
			sb.WriteString(fmt.Sprintf("  [%s] %s\r\n    %v\r\n", r.Section, r.Title, r.Err))
			if r.Link != "" {
				sb.WriteString("    открыть в кабинете: " + r.Link + "\r\n")
			}
		}
		sb.WriteString("\r\n")
	}

	sb.WriteString(fmt.Sprintf("Итого: изменилось %d, без изменений %d, с ошибкой %d.\r\n",
		len(updated), len(unchanged), len(failed)))
	return sb.String()
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// --- сохранение содержимого лент ---

var reUnsafeName = regexp.MustCompile(`[^\p{L}\p{N}._ -]+`)

// safeFileName убирает из имени всё, что может увести запись за пределы папки.
func safeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = reUnsafeName.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .")
	if name == "" || name == "_" {
		name = "file"
	}
	if len(name) > 150 {
		name = name[:150]
	}
	return name
}

// SaveMessageText кладёт рядом с вложениями текст сообщения.
func SaveMessageText(dir, date string, msg MessageContent) error {
	var sb strings.Builder
	sb.WriteString("Дата сообщения: " + date + "\r\n")
	if msg.Subject != "" {
		sb.WriteString("Тема: " + msg.Subject + "\r\n")
	}
	sb.WriteString("\r\n")
	sb.WriteString(stripTags(msg.Body))
	sb.WriteString("\r\n")
	return writeFileAtomic(filepath.Join(dir, "message.txt"), []byte(sb.String()))
}

// SaveAttachment сохраняет вложение, не перезаписывая уже существующее.
func SaveAttachment(dir, fileName, ext string, data []byte) (string, error) {
	name := safeFileName(fileName)
	if filepath.Ext(name) == "" && ext != "" {
		name += ext
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		base := strings.TrimSuffix(name, filepath.Ext(name))
		name = fmt.Sprintf("%s_%d%s", base, time.Now().Unix(), filepath.Ext(name))
		path = filepath.Join(dir, name)
	}
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return name, nil
}

var reTags = regexp.MustCompile(`<[^>]*>`)

// stripTags приводит HTML-текст сообщения к простому тексту.
func stripTags(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\r\n")
	s = strings.ReplaceAll(s, "<br/>", "\r\n")
	s = strings.ReplaceAll(s, "<br />", "\r\n")
	s = strings.ReplaceAll(s, "</p>", "\r\n")
	s = reTags.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&laquo;", "«")
	s = strings.ReplaceAll(s, "&raquo;", "»")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.TrimSpace(s)
}

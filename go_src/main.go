package main

// Автоматическое отслеживание перечней Росфинмониторинга.
//
// Главный принцип: скачиваем только то, что действительно изменилось.
//
// Каталоги (terrorism, mvk, oon) — свежесть по ленте событий кабинета:
// один запрос GetNotifications, сравниваем createDate с state.json.
// Ленты (court, info) — свежесть по дате верхнего сообщения раздела.
//
// Если не изменилось ничего — папка прогона не создаётся вообще.
// После успешного скачивания каталога его уведомления помечаются
// прочитанными: так поведение повторяет человека, который зашёл,
// увидел новое и забрал его.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Login      string
	Password   string
	BaseURL    string
	OutputDir  string
	Sources    []Source
	Feeds      []Feed
	Windows    []Window
	Timeout    time.Duration
	Attempts   int
	KeepRuns   int
	Extract    bool
	Once       bool
	MakeLatest bool
	MarkRead   bool
	ForceAll   bool

	SMTPHost    string
	SMTPAddr    string
	SMTPPort    int
	SMTPUser    string
	SMTPPass    string
	MailTo      string
	MailEnabled bool
	MailOnQuiet bool
}

func parseConfig() (Config, error) {
	var (
		login      = flag.String("login", "", "логин личного кабинета (или ROSFIN_LOGIN)")
		password   = flag.String("password", "", "пароль личного кабинета (или ROSFIN_PASS)")
		baseURL    = flag.String("base-url", envOr("ROSFIN_BASE_URL", ""), "адрес портала")
		outputDir  = flag.String("output", envOr("ROSFIN_OUTPUT_DIR", "./downloads"), "папка для скачанных файлов")
		sourcesRaw = flag.String("sources", envOr("ROSFIN_SOURCES", "all"), "каталоги: all или xml,doc,mvk,oon-rus,oon-eng")
		feedsRaw   = flag.String("feeds", envOr("ROSFIN_FEEDS", "all"), "ленты: all, none или court,info")
		windowsRaw = flag.String("windows", envOr("ROSFIN_WINDOWS", "09:00-10:00,17:00-18:00"), "окна ежедневного запуска")
		timeout    = flag.Duration("timeout", envDuration("ROSFIN_TIMEOUT", 3*time.Minute), "таймаут HTTP-запросов")
		attempts   = flag.Int("attempts", envInt("ROSFIN_ATTEMPTS", 3), "число попыток при ошибке")
		keepRuns   = flag.Int("keep", envInt("ROSFIN_KEEP_RUNS", 0), "сколько последних папок хранить (0 — всё)")
		extract    = flag.Bool("extract", envBool("ROSFIN_EXTRACT", true), "распаковывать архивы")
		once       = flag.Bool("once", envBool("ROSFIN_ONCE", false), "один прогон и выход")
		makeLatest = flag.Bool("latest-link", envBool("ROSFIN_LATEST_LINK", true), "обновлять симлинк latest")
		markRead   = flag.Bool("mark-read", envBool("ROSFIN_MARK_READ", true), "отмечать уведомления прочитанными после скачивания")
		forceAll   = flag.Bool("force", envBool("ROSFIN_FORCE", false), "скачать всё, игнорируя проверку изменений")

		courtType = flag.Int("court-message-type", envInt("ROSFIN_COURT_MESSAGE_TYPE", 67), "IdMessageType ленты «Решения судов»")
		infoType  = flag.Int("info-message-type", envInt("ROSFIN_INFO_MESSAGE_TYPE", 11), "IdMessageType ленты «Информация Росфинмониторинга»")

		// Почтовые параметры по умолчанию пустые: их задают в .env / docker-compose.
		smtpHost  = flag.String("smtp-host", envOr("ROSFIN_SMTP_HOST", ""), "имя почтового сервера (для TLS и HELO)")
		smtpAddr  = flag.String("smtp-addr", envOr("ROSFIN_SMTP_ADDR", ""), "адрес для подключения к SMTP; пусто — использовать smtp-host")
		smtpPort  = flag.Int("smtp-port", envInt("ROSFIN_SMTP_PORT", 587), "порт SMTP")
		smtpUser  = flag.String("smtp-user", envOr("ROSFIN_SMTP_USER", ""), "логин (адрес) отправителя")
		smtpPass  = flag.String("smtp-pass", envOr("ROSFIN_SMTP_PASS", ""), "пароль отправителя (лучше через ROSFIN_SMTP_PASS)")
		mailTo    = flag.String("mail-to", envOr("ROSFIN_MAIL_TO", ""), "получатель уведомлений")
		mailOn    = flag.Bool("mail-enabled", envBool("ROSFIN_MAIL_ENABLED", true), "отправлять письмо по итогам запуска")
		mailQuiet = flag.Bool("mail-on-quiet", envBool("ROSFIN_MAIL_ON_QUIET", true),
			"писать письмо и тогда, когда изменений нет (тишина в почте будет означать поломку)")
	)
	flag.Parse()

	cfg := Config{
		Login:      firstNonEmpty(*login, os.Getenv("ROSFIN_LOGIN")),
		Password:   firstNonEmpty(*password, os.Getenv("ROSFIN_PASS")),
		BaseURL:    *baseURL,
		OutputDir:  *outputDir,
		Timeout:    *timeout,
		Attempts:   *attempts,
		KeepRuns:   *keepRuns,
		Extract:    *extract,
		Once:       *once,
		MakeLatest: *makeLatest,
		MarkRead:   *markRead,
		ForceAll:   *forceAll,

		SMTPHost: *smtpHost,
		SMTPAddr: *smtpAddr,
		SMTPPort: *smtpPort,
		SMTPUser: *smtpUser,
		SMTPPass: *smtpPass,
		MailTo:   *mailTo,
		// Почта включается, только если заданы сервер, отправитель, получатель и пароль.
		MailEnabled: *mailOn && *smtpHost != "" && *smtpUser != "" && *mailTo != "" && *smtpPass != "",
		MailOnQuiet: *mailQuiet,
	}

	if cfg.Login == "" || cfg.Password == "" {
		return cfg, errors.New("не заданы логин и пароль личного кабинета (ROSFIN_LOGIN / ROSFIN_PASS)")
	}

	sources, err := parseSources(*sourcesRaw)
	if err != nil {
		return cfg, err
	}
	cfg.Sources = sources

	feeds, err := parseFeeds(*feedsRaw, *courtType, *infoType)
	if err != nil {
		return cfg, err
	}
	cfg.Feeds = feeds

	windows, err := ParseWindows(*windowsRaw)
	if err != nil {
		return cfg, fmt.Errorf("windows: %w", err)
	}
	cfg.Windows = windows

	return cfg, nil
}

func parseSources(raw string) ([]Source, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "all") {
		return DefaultSources, nil
	}
	var out []Source
	for _, name := range strings.Split(raw, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if name == "word" {
			name = "doc"
		}
		s, ok := SourceByName(name)
		if !ok {
			return nil, fmt.Errorf("неизвестный каталог %q (доступны: %s)", name, strings.Join(SourceNames(), ", "))
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("не выбран ни один каталог")
	}
	return out, nil
}

func parseFeeds(raw string, courtType, infoType int) ([]Feed, error) {
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "none") {
		return nil, nil
	}
	pick := DefaultFeeds
	if raw != "" && !strings.EqualFold(raw, "all") {
		pick = nil
		for _, name := range strings.Split(raw, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			f, ok := FeedByName(name)
			if !ok {
				return nil, fmt.Errorf("неизвестная лента %q (доступны: court, info)", name)
			}
			pick = append(pick, f)
		}
	}
	out := make([]Feed, 0, len(pick))
	for _, f := range pick {
		switch f.Name {
		case "court":
			f.IdMessageType = courtType
		case "info":
			f.IdMessageType = infoType
		}
		out = append(out, f)
	}
	return out, nil
}

func withRetry(attempts int, what string, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	delay := 5 * time.Second
	for i := 1; i <= attempts; i++ {
		if lastErr = fn(); lastErr == nil {
			return nil
		}
		log.Printf("%s: попытка %d/%d не удалась: %v (ждём %s)", what, i, attempts, lastErr, delay)
		if i < attempts {
			time.Sleep(delay)
			delay *= 2
		}
	}
	return fmt.Errorf("%s: все %d попыток не удались: %w", what, attempts, lastErr)
}

func runOnce(cfg Config) error {
	startedAt := time.Now()

	state, err := LoadState(cfg.OutputDir)
	if err != nil {
		log.Printf("предупреждение: не удалось прочитать state.json (%v), считаем что истории нет", err)
	}

	client, err := NewClient(cfg.Login, cfg.Password, cfg.BaseURL, cfg.Timeout)
	if err != nil {
		return fmt.Errorf("клиент: %w", err)
	}
	if err := client.Login(); err != nil {
		return fmt.Errorf("вход: %w", err)
	}

	var results []ItemResult

	// --- Шаг 1: что изменилось в каталогах ---
	notifications, notifErr := client.Notifications(200)
	latest, notifIDs := LatestBySection(notifications)
	if notifErr != nil {
		// Без ленты событий понять, что изменилось, нельзя.
		// Осторожная стратегия: качаем всё, но честно пишем об этом.
		log.Printf("ОШИБКА: %v — качаем все каталоги на всякий случай", notifErr)
	}

	needCatalog := map[string]bool{}
	for _, s := range cfg.Sources {
		switch {
		case cfg.ForceAll:
			needCatalog[s.Section] = true
		case notifErr != nil:
			needCatalog[s.Section] = true
		default:
			newDate := latest[s.Section]
			if newDate == "" {
				// Уведомлений по разделу нет вовсе — вести отсчёт не от чего.
				// Качаем только если раньше не забирали.
				needCatalog[s.Section] = state.Catalogs[s.Section] == ""
			} else {
				needCatalog[s.Section] = newDate > state.Catalogs[s.Section]
			}
		}
	}

	// --- Шаг 2: что изменилось в лентах ---
	feedHead := map[string]FeedHead{}
	needFeed := map[string]bool{}
	for _, f := range cfg.Feeds {
		head, err := client.FeedLatest(f)
		if err != nil {
			log.Printf("ОШИБКА: %v", err)
			results = append(results, ItemResult{
				Section: f.Section, Title: f.Title, Status: StatusError, Err: err,
			})
			continue
		}
		feedHead[f.Name] = head
		needFeed[f.Name] = cfg.ForceAll || FeedNewer(head.Date, state.Feeds[f.Name])
	}

	changed := false
	for _, v := range needCatalog {
		if v {
			changed = true
		}
	}
	for _, v := range needFeed {
		if v {
			changed = true
		}
	}

	// --- Шаг 3: если ничего не изменилось, папку не создаём ---
	if !changed {
		for _, s := range cfg.Sources {
			results = append(results, ItemResult{Section: s.Section, Title: s.Title, Status: StatusUnchanged})
		}
		for _, f := range cfg.Feeds {
			if _, ok := feedHead[f.Name]; ok {
				results = append(results, ItemResult{Section: f.Section, Title: f.Title, Status: StatusUnchanged})
			}
		}
		log.Printf("изменений нет, скачивание не требуется (%s)", time.Since(startedAt).Round(time.Millisecond))
		if cfg.MailOnQuiet {
			if err := sendMail(cfg, "rosfin: изменений нет", summarizeRun("", results)); err != nil {
				log.Printf("почта: не удалось отправить уведомление: %v", err)
			}
		}
		return nil
	}

	dir := RunDir(cfg.OutputDir, startedAt)

	// --- Шаг 4: каталоги ---
	var manifest Manifest
	okSections := map[string]bool{}

	for _, s := range cfg.Sources {
		if !needCatalog[s.Section] {
			results = append(results, ItemResult{Section: s.Section, Title: s.Title, Status: StatusUnchanged})
			continue
		}

		sectionDir := filepath.Join(dir, s.Section)
		if err := os.MkdirAll(sectionDir, 0o755); err != nil {
			return fmt.Errorf("папка %s: %w", sectionDir, err)
		}

		var data []byte
		var ext string
		err := withRetry(cfg.Attempts, "скачивание "+s.Name, func() error {
			var e error
			data, ext, e = client.DownloadSource(s)
			return e
		})
		if err != nil {
			log.Printf("ОШИБКА: %v", err)
			results = append(results, ItemResult{Section: s.Section, Title: s.Title, Status: StatusError, Err: err})
			continue
		}

		saved, err := SaveArchive(sectionDir, s.AsFormat(), data, ext)
		if err != nil {
			log.Printf("ОШИБКА: сохранение %s: %v", s.Name, err)
			results = append(results, ItemResult{Section: s.Section, Title: s.Title, Status: StatusError, Err: err})
			continue
		}

		if cfg.Extract && strings.EqualFold(ext, ".zip") {
			names, err := ExtractZip(sectionDir, data)
			if err != nil {
				log.Printf("предупреждение: не удалось распаковать %s: %v", saved.Archive, err)
			} else {
				saved.Extracted = names
			}
		}

		manifest.Files = append(manifest.Files, saved)
		okSections[s.Section] = true
		results = append(results, ItemResult{
			Section: s.Section, Title: s.Title, Status: StatusUpdated, Saved: saved,
		})
		log.Printf("сохранено: %s/%s (%.1f МБ, sha256 %s...)",
			s.Section, saved.Archive, float64(saved.Bytes)/(1<<20), saved.SHA256[:12])
	}

	// --- Шаг 5: ленты ---
	for _, f := range cfg.Feeds {
		head, ok := feedHead[f.Name]
		if !ok {
			continue
		}
		if !needFeed[f.Name] {
			results = append(results, ItemResult{Section: f.Section, Title: f.Title, Status: StatusUnchanged})
			continue
		}

		res := ItemResult{Section: f.Section, Title: f.Title, Status: StatusUpdated, Note: "сообщение от " + head.Date}

		// Раздел, содержимое которого автоматически забрать нельзя
		// (messageId живёт во внутреннем состоянии грида DevExpress):
		// сообщаем об обновлении и даём прямую ссылку на кабинет.
		if !f.CanFetch {
			res.Link = FeedURL(f)
			res.Note = "появилось новое сообщение от " + head.Date + " — откройте раздел в кабинете"
			state.Feeds[f.Name] = head.Date
			results = append(results, res)
			log.Printf("обновление: %s (%s) — забрать автоматически нельзя, ссылка в письме", f.Title, head.Date)
			continue
		}

		if len(head.MessageIDs) == 0 {
			err := fmt.Errorf("лента %s: не удалось определить messageId", f.Name)
			log.Printf("ОШИБКА: %v", err)
			results = append(results, ItemResult{
				Section: f.Section, Title: f.Title, Status: StatusError, Err: err, Link: FeedURL(f),
			})
			continue
		}

		// Пробуем кандидатов по очереди: правильный тот, на котором
		// портал вернёт содержимое вместо ошибки.
		var msg MessageContent
		var messageID string
		var lastErr error
		for _, id := range head.MessageIDs {
			m, err := client.OpenMessage(f, id)
			if err == nil {
				msg, messageID = m, id
				break
			}
			lastErr = err
		}
		if messageID == "" {
			log.Printf("ОШИБКА: лента %s: ни один из %d кандидатов не подошёл, последняя ошибка: %v",
				f.Name, len(head.MessageIDs), lastErr)
			results = append(results, ItemResult{
				Section: f.Section, Title: f.Title, Status: StatusError, Err: lastErr, Link: FeedURL(f),
			})
			continue
		}

		sectionDir := filepath.Join(dir, f.Section)
		if err := os.MkdirAll(sectionDir, 0o755); err != nil {
			return fmt.Errorf("папка %s: %w", sectionDir, err)
		}

		shownDate := FeedDisplayDate(head.Date, msg.SentDate)
		res.Note = "сообщение от " + shownDate
		if err := SaveMessageText(sectionDir, shownDate, msg); err != nil {
			log.Printf("предупреждение: не удалось сохранить текст сообщения %s: %v", f.Name, err)
		}

		for _, a := range msg.Attachments {
			data, ext, err := client.DownloadAttachment(messageID, a.FileID)
			if err != nil {
				log.Printf("предупреждение: вложение %s (%s): %v", a.FileName, f.Name, err)
				continue
			}
			name, err := SaveAttachment(sectionDir, a.FileName, ext, data)
			if err != nil {
				log.Printf("предупреждение: сохранение вложения %s: %v", a.FileName, err)
				continue
			}
			res.Files = append(res.Files, name)
			log.Printf("сохранено: %s/%s (%.1f МБ)", f.Section, name, float64(len(data))/(1<<20))
		}

		state.Feeds[f.Name] = head.Date
		results = append(results, res)
	}

	// --- Шаг 6: манифест, симлинк, чистка ---
	if len(manifest.Files) > 0 {
		if err := WriteManifest(dir, manifest); err != nil {
			log.Printf("предупреждение: не удалось записать манифест: %v", err)
		}
	}
	if cfg.MakeLatest {
		if err := UpdateLatestLink(cfg.OutputDir, dir); err != nil {
			log.Printf("предупреждение: не удалось обновить симлинк latest: %v", err)
		}
	}

	// --- Шаг 7: отметка прочитанным — только за реально скачанное ---
	if cfg.MarkRead && notifErr == nil {
		var toMark []string
		for section := range okSections {
			toMark = append(toMark, UnreadIDs(notifications, notifIDs[section])...)
		}
		if len(toMark) > 0 {
			if err := client.MarkRead(toMark); err != nil {
				log.Printf("предупреждение: %v", err)
			} else {
				log.Printf("отмечено прочитанными уведомлений: %d", len(toMark))
			}
		}
	}

	// --- Шаг 8: запоминаем даты только по успешно забранному ---
	for section := range okSections {
		if d := latest[section]; d != "" {
			state.Catalogs[section] = d
		}
	}
	if err := SaveState(cfg.OutputDir, state); err != nil {
		log.Printf("предупреждение: не удалось сохранить state.json: %v", err)
	}

	if removed, err := PruneOldRuns(cfg.OutputDir, cfg.KeepRuns); err != nil {
		log.Printf("предупреждение: не удалось почистить старые папки: %v", err)
	} else if len(removed) > 0 {
		log.Printf("удалено старых папок: %d", len(removed))
	}

	upd, unch, failed := countStatuses(results)

	// Путь к папке показываем только если в неё что-то реально легло:
	// при пустом прогоне папка не создаётся, и печатать её путь — врать.
	savedDir := dir
	if upd == 0 {
		savedDir = ""
	}
	if savedDir != "" {
		log.Printf("готово за %s → %s (обновлено %d, без изменений %d, с ошибкой %d)",
			time.Since(startedAt).Round(time.Millisecond), savedDir, upd, unch, failed)
	} else {
		log.Printf("готово за %s (обновлено %d, без изменений %d, с ошибкой %d)",
			time.Since(startedAt).Round(time.Millisecond), upd, unch, failed)
	}

	subject := fmt.Sprintf("rosfin: обновлено разделов — %d", upd)
	if failed > 0 {
		subject = fmt.Sprintf("rosfin: обновлено %d, с ошибкой %d", upd, failed)
	}
	if err := sendMail(cfg, subject, summarizeRun(savedDir, results)); err != nil {
		log.Printf("почта: не удалось отправить уведомление: %v", err)
	}

	// Прогон считается неудачным, только если сломанные разделы реально были.
	// Шесть разделов «без изменений» — это норма, а не провал.
	if failed > 0 {
		var broken []string
		for _, r := range results {
			if r.Status == StatusError {
				broken = append(broken, r.Section)
			}
		}
		return fmt.Errorf("не удалось обработать разделы: %s", strings.Join(broken, ", "))
	}
	return nil
}

func runForever(ctx context.Context, cfg Config) error {
	log.Printf("демон запущен, окна: %s, каталогов: %d, лент: %d, папка %s",
		FormatWindows(cfg.Windows), len(cfg.Sources), len(cfg.Feeds), cfg.OutputDir)
	if !cfg.MailEnabled {
		log.Printf("почтовые уведомления выключены (не заданы ROSFIN_SMTP_HOST / ROSFIN_SMTP_USER / ROSFIN_SMTP_PASS / ROSFIN_MAIL_TO)")
	}
	sched := NewScheduler(cfg.Windows)
	for {
		target, idx := sched.Next(time.Now())
		log.Printf("следующая проверка: %s", target.Format(time.RFC3339))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(target)):
		}
		sched.MarkRun(idx, time.Now())
		if err := runOnce(cfg); err != nil {
			log.Printf("ОШИБКА: %v", err)
			if merr := sendMail(cfg, "rosfin: ошибка запуска",
				fmt.Sprintf("Запуск завершился с ошибкой:\r\n%v", err)); merr != nil {
				log.Printf("почта: не удалось отправить уведомление об ошибке: %v", merr)
			}
		}
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("rosfin: ")

	cfg, err := parseConfig()
	if err != nil {
		log.Printf("ошибка конфигурации: %v", err)
		flag.Usage()
		os.Exit(1)
	}

	if cfg.Once {
		if err := runOnce(cfg); err != nil {
			log.Printf("запуск завершился с ошибкой: %v", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runForever(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("остановлено с ошибкой: %v", err)
		os.Exit(1)
	}
	log.Print("остановлено по сигналу, выходим")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

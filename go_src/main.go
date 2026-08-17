// Утилита для автоматического скачивания из личного кабинета Росфинмониторинга
// перечня организаций и физических лиц, в отношении которых имеются сведения
// об их причастности к экстремистской деятельности или терроризму.
//
// Скачиваются два формата актуального перечня:
//   - XML (схема 2.1) — /SkedDownload/GetActiveSked?type=xml21
//   - Word (*.doc)    — /SkedDownload/GetActiveSked?type=doc
//
// По умолчанию работает как демон: скачивает сразу при старте
// и далее каждые 12 часов.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config — параметры запуска.
type Config struct {
	Login      string
	Password   string
	BaseURL    string
	OutputDir  string
	Formats    []Format
	Interval   time.Duration
	Timeout    time.Duration
	Attempts   int
	KeepRuns   int
	Extract    bool
	MarkAsRead bool
	Once       bool
	MakeLatest bool
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("rosfin: ")

	cfg, err := parseConfig()
	if err != nil {
		log.Printf("ошибка конфигурации: %v", err)
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Once {
		if err := runOnce(cfg); err != nil {
			log.Printf("запуск завершился с ошибкой: %v", err)
			os.Exit(1)
		}
		return
	}

	if err := runForever(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("остановлено с ошибкой: %v", err)
		os.Exit(1)
	}
	log.Print("остановлено по сигналу, выходим")
}

// runForever скачивает перечень сразу при старте и далее по интервалу.
// Ошибка отдельного запуска не роняет процесс — ждём следующего тика.
func runForever(ctx context.Context, cfg Config) error {
	log.Printf("демон запущен, интервал %s, папка %s", cfg.Interval, cfg.OutputDir)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		if err := runOnce(cfg); err != nil {
			log.Printf("ОШИБКА: %v (повтор через %s)", err, cfg.Interval)
		}
		log.Printf("следующий запуск: %s", time.Now().Add(cfg.Interval).Format(time.RFC3339))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// runOnce выполняет один полный цикл: вход, скачивание всех форматов, сохранение.
func runOnce(cfg Config) error {
	startedAt := time.Now()
	dir := RunDir(cfg.OutputDir, startedAt)

	client, err := NewClient(cfg.Login, cfg.Password, cfg.BaseURL, cfg.Timeout)
	if err != nil {
		return err
	}

	if err := withRetry(cfg.Attempts, "вход в кабинет", client.Login); err != nil {
		return err
	}
	log.Print("вход выполнен")

	if cfg.MarkAsRead {
		if n, err := client.MarkNotificationsRead(); err != nil {
			log.Printf("предупреждение: не удалось отметить уведомления: %v", err)
		} else if n > 0 {
			log.Printf("отмечено прочитанными уведомлений: %d", n)
		}
	}

	manifest := Manifest{
		DownloadedAt: startedAt.Format(time.RFC3339),
		Source:       client.rootURL + "/SkedDownload/GetActiveSked",
	}

	for _, f := range cfg.Formats {
		var (
			data []byte
			ext  string
		)
		err := withRetry(cfg.Attempts, "скачивание "+f.Name, func() error {
			var e error
			data, ext, e = client.Download(f)
			return e
		})
		if err != nil {
			return err
		}

		saved, err := SaveArchive(dir, f, data, ext)
		if err != nil {
			return err
		}

		if cfg.Extract && strings.EqualFold(ext, ".zip") {
			names, err := ExtractZip(dir, data)
			if err != nil {
				log.Printf("предупреждение: не удалось распаковать %s: %v", saved.Archive, err)
			} else {
				saved.Extracted = names
			}
		}

		manifest.Files = append(manifest.Files, saved)
		log.Printf("сохранено: %s (%.1f МБ, sha256 %s…)",
			saved.Archive, float64(saved.Bytes)/(1<<20), saved.SHA256[:12])
	}

	if err := WriteManifest(dir, manifest); err != nil {
		return err
	}

	if cfg.MakeLatest {
		if err := UpdateLatestLink(cfg.OutputDir, dir); err != nil {
			log.Printf("предупреждение: не удалось обновить ссылку latest: %v", err)
		}
	}

	if removed, err := PruneOldRuns(cfg.OutputDir, cfg.KeepRuns); err != nil {
		log.Printf("предупреждение: не удалось почистить старые папки: %v", err)
	} else if len(removed) > 0 {
		log.Printf("удалено старых папок: %d", len(removed))
	}

	log.Printf("готово за %s → %s", time.Since(startedAt).Round(time.Millisecond), dir)
	return nil
}

// withRetry повторяет операцию с экспоненциальной задержкой.
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
		if i < attempts {
			log.Printf("%s: попытка %d/%d не удалась: %v (ждём %s)", what, i, attempts, lastErr, delay)
			time.Sleep(delay)
			delay *= 2
		}
	}
	return fmt.Errorf("%s: все %d попыток не удались: %w", what, attempts, lastErr)
}

func parseConfig() (Config, error) {
	var (
		login      = flag.String("login", "", "логин личного кабинета (или ROSFIN_LOGIN)")
		password   = flag.String("password", "", "пароль личного кабинета (или ROSFIN_PASS)")
		baseURL    = flag.String("base-url", envOr("ROSFIN_BASE_URL", ""), "адрес портала (по умолчанию "+rootURL+"), нужен только для тестов")
		outputDir  = flag.String("output", envOr("ROSFIN_OUTPUT_DIR", "./downloads"), "папка для скачанных файлов")
		formatsRaw = flag.String("formats", envOr("ROSFIN_FORMATS", "xml,doc"), "форматы через запятую: xml, doc")
		interval   = flag.Duration("interval", envDuration("ROSFIN_INTERVAL", 12*time.Hour), "интервал между скачиваниями")
		timeout    = flag.Duration("timeout", envDuration("ROSFIN_TIMEOUT", 3*time.Minute), "таймаут HTTP-запроса")
		attempts   = flag.Int("attempts", envInt("ROSFIN_ATTEMPTS", 3), "число попыток при ошибке")
		keepRuns   = flag.Int("keep", envInt("ROSFIN_KEEP_RUNS", 0), "сколько последних папок хранить (0 — хранить все)")
		extract    = flag.Bool("extract", envBool("ROSFIN_EXTRACT", true), "распаковывать архивы рядом с ними")
		markRead   = flag.Bool("mark-notifications", envBool("ROSFIN_MARK_NOTIFICATIONS", true), "отмечать уведомления кабинета прочитанными")
		once       = flag.Bool("once", envBool("ROSFIN_ONCE", false), "скачать один раз и выйти")
		makeLatest = flag.Bool("latest-link", envBool("ROSFIN_LATEST_LINK", true), "обновлять симлинк downloads/latest")
	)
	flag.Parse()

	cfg := Config{
		Login:      firstNonEmpty(*login, os.Getenv("ROSFIN_LOGIN")),
		Password:   firstNonEmpty(*password, os.Getenv("ROSFIN_PASS")),
		BaseURL:    *baseURL,
		OutputDir:  *outputDir,
		Interval:   *interval,
		Timeout:    *timeout,
		Attempts:   *attempts,
		KeepRuns:   *keepRuns,
		Extract:    *extract,
		MarkAsRead: *markRead,
		Once:       *once,
		MakeLatest: *makeLatest,
	}

	if cfg.Login == "" || cfg.Password == "" {
		return cfg, errors.New("не заданы логин и пароль (флаги -login/-password или ROSFIN_LOGIN/ROSFIN_PASS)")
	}
	if cfg.Interval < time.Minute {
		return cfg, errors.New("интервал должен быть не меньше минуты")
	}

	for _, name := range strings.Split(*formatsRaw, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if name == "word" {
			name = "doc"
		}
		f, ok := KnownFormats[name]
		if !ok {
			return cfg, fmt.Errorf("неизвестный формат %q (доступны: xml, doc)", name)
		}
		cfg.Formats = append(cfg.Formats, f)
	}
	if len(cfg.Formats) == 0 {
		return cfg, errors.New("не выбран ни один формат")
	}

	return cfg, nil
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

// Command ginbot-migrate is the one-shot TohsakaBot -> GinBot data migration.
// It reads a mysqldump .sql file, not a live database. See docs/plans/migration.md.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/storage"
	"github.com/lasikuu/GinBot/pkg/trigger"
	"go.uber.org/zap"
)

const (
	discordPlatform      = int32(1)
	triggerFileKeyPrefix = "trigger/"
	fallbackInstanceUID  = "migration-unresolved"
)

func main() {
	source := flag.String("source", "rin/tohsaka_full_backup.sql", "path to the mysqldump .sql file")
	dataDir := flag.String("source-data-dir", "rin", "directory holding servers.json and triggers/")
	dryRun := flag.Bool("dry-run", true, "report without writing; --dry-run=false to commit")
	flag.Parse()

	config.LoadEnv()
	log.InitializeLogger(config.AppEnvironment, config.LogLevel)
	defer log.Sync()
	config.SetEnv()

	db.InitDB()
	db.EnsureLatestVersion()
	db.CloseDB()

	if err := storage.Init(config.Options.Storage.Path); err != nil {
		log.Z.Fatal("failed to initialize storage.", zap.Error(err))
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		log.Z.Fatal("failed to connect.", zap.Error(err))
	}
	defer pool.Close()

	if err := run(ctx, pool, *source, *dataDir, *dryRun); err != nil {
		log.Z.Fatal("migration failed.", zap.Error(err))
	}
}

func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.Options.DB.Username, config.Options.DB.Password),
		Host:   net.JoinHostPort(config.Options.DB.Host, strconv.Itoa(int(config.Options.DB.Port))),
		Path:   config.Options.DB.Name,
	}
	return pgxpool.New(ctx, dsn.String())
}

func run(ctx context.Context, pool *pgxpool.Pool, source, dataDir string, dryRun bool) error {
	tables, err := parseDump(source)
	if err != nil {
		return err
	}
	servers, err := loadServers(filepath.Join(dataDir, "servers.json"))
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	m := &migrator{
		ctx: ctx, tx: tx, dryRun: dryRun,
		dataDir: dataDir, tables: tables, servers: servers,
		blobs:       storage.Default(),
		idMap:       map[string]map[string]string{},
		users:       map[string]string{},
		platformUID: map[string]string{},
		instances:   map[string]int64{},
		channels:    map[string]int64{},
		files:       map[string]string{},
		triggers:    map[string]string{},
		reminders:   map[string]string{},
	}

	if err := m.ensureScratch(); err != nil {
		return err
	}
	if err := m.loadIDMap(); err != nil {
		return err
	}

	passes := []struct {
		name string
		fn   func() error
	}{
		{"1 instances", m.passInstances},
		{"2 users", m.passUsers},
		{"3 platform identities", m.passPlatformUsers},
		{"4 destinations", m.passDestinations},
		{"5 files", m.passFiles},
		{"6 triggers", m.passTriggers},
		{"7 trigger stats", m.passTriggerStats},
		{"8 reminders", m.passReminders},
		{"9 deferred staging", m.passStaging},
	}
	for _, p := range passes {
		if err := p.fn(); err != nil {
			return fmt.Errorf("pass %s: %w", p.name, err)
		}
	}

	m.printReport()

	if dryRun {
		fmt.Println("\nDRY RUN - rolled back. Re-run with --dry-run=false to commit.")
		return nil
	}
	return tx.Commit(ctx)
}

type value struct {
	s    string
	null bool
}

// parseDump errors on anything it does not fully understand, never guessing.
func parseDump(path string) (map[string][][]value, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src := string(raw)
	out := map[string][][]value{}

	const marker = "INSERT INTO `"
	for i := 0; ; {
		j := strings.Index(src[i:], marker)
		if j < 0 {
			break
		}
		i += j + len(marker)

		end := strings.IndexByte(src[i:], '`')
		if end < 0 {
			return nil, fmt.Errorf("unterminated table name at offset %d", i)
		}
		table := src[i : i+end]
		i += end + 1

		const values = " VALUES"
		if !strings.HasPrefix(src[i:], values) {
			return nil, fmt.Errorf("table %s: expected VALUES at offset %d", table, i)
		}
		i += len(values)

		rows, next, err := parseTuples(src, i, table)
		if err != nil {
			return nil, err
		}
		out[table] = append(out[table], rows...)
		i = next
	}
	return out, nil
}

func parseTuples(src string, i int, table string) ([][]value, int, error) {
	var rows [][]value
	for {
		i = skipSpace(src, i)
		if i >= len(src) || src[i] != '(' {
			return nil, 0, fmt.Errorf("table %s: expected '(' at %s", table, at(src, i))
		}
		i++

		var row []value
		for {
			var (
				v   value
				err error
			)
			v, i, err = parseValue(src, skipSpace(src, i), table)
			if err != nil {
				return nil, 0, err
			}
			row = append(row, v)

			i = skipSpace(src, i)
			if i >= len(src) {
				return nil, 0, fmt.Errorf("table %s: truncated tuple", table)
			}
			if src[i] == ',' {
				i++
				continue
			}
			if src[i] == ')' {
				i++
				break
			}
			return nil, 0, fmt.Errorf("table %s: expected ',' or ')' at %s", table, at(src, i))
		}
		rows = append(rows, row)

		i = skipSpace(src, i)
		if i >= len(src) {
			return nil, 0, fmt.Errorf("table %s: missing statement terminator", table)
		}
		if src[i] == ',' {
			i++
			continue
		}
		if src[i] == ';' {
			return rows, i + 1, nil
		}
		return nil, 0, fmt.Errorf("table %s: expected ',' or ';' at %s", table, at(src, i))
	}
}

func parseValue(src string, i int, table string) (value, int, error) {
	if i >= len(src) {
		return value{}, 0, fmt.Errorf("table %s: truncated value", table)
	}
	if src[i] == '\'' {
		var b strings.Builder
		i++
		for {
			if i >= len(src) {
				return value{}, 0, fmt.Errorf("table %s: unterminated string", table)
			}
			c := src[i]
			if c == '\\' {
				if i+1 >= len(src) {
					return value{}, 0, fmt.Errorf("table %s: truncated escape", table)
				}
				b.WriteString(unescape(src[i+1]))
				i += 2
				continue
			}
			if c == '\'' {
				// Doubled quote is a literal quote; mysqldump normally emits \'.
				if i+1 < len(src) && src[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				return value{s: b.String()}, i + 1, nil
			}
			b.WriteByte(c)
			i++
		}
	}

	start := i
	for i < len(src) && src[i] != ',' && src[i] != ')' {
		i++
	}
	lit := strings.TrimSpace(src[start:i])
	if lit == "" {
		return value{}, 0, fmt.Errorf("table %s: empty value at %s", table, at(src, start))
	}
	if strings.EqualFold(lit, "NULL") {
		return value{null: true}, i, nil
	}
	if !isNumeric(lit) {
		return value{}, 0, fmt.Errorf("table %s: unsupported literal %q at %s", table, lit, at(src, start))
	}
	return value{s: lit}, i, nil
}

func unescape(c byte) string {
	switch c {
	case '0':
		return "\x00"
	case 'b':
		return "\b"
	case 'n':
		return "\n"
	case 'r':
		return "\r"
	case 't':
		return "\t"
	case 'Z':
		return "\x1a"
	default:
		return string(c)
	}
}

func isNumeric(s string) bool {
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}

func skipSpace(src string, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\n' || src[i] == '\r' || src[i] == '\t') {
		i++
	}
	return i
}

func at(src string, i int) string {
	return fmt.Sprintf("offset %d (line %d)", i, strings.Count(src[:min(i, len(src))], "\n")+1)
}

type server struct {
	ID               int64 `json:"id"`
	DefaultChannel   int64 `json:"default_channel"`
	HighlightChannel int64 `json:"highlight_channel"`
}

func loadServers(path string) ([]server, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Servers []server `json:"servers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc.Servers, nil
}

type migrator struct {
	ctx     context.Context
	tx      pgx.Tx
	dryRun  bool
	dataDir string
	tables  map[string][][]value
	servers []server
	blobs   storage.Storage

	idMap       map[string]map[string]string
	users       map[string]string // legacy user id -> uuid
	platformUID map[string]string // legacy user id -> discord uid
	instances   map[string]int64  // guild snowflake -> instance id
	channels    map[string]int64  // channel snowflake -> destination id
	files       map[string]string // legacy filename -> file uuid
	triggers    map[string]string // legacy trigger id -> uuid
	reminders   map[string]string // legacy reminder id -> uuid
	fallback    int64

	reports []*report
}

type report struct {
	name                   string
	read, written, skipped int
	reasons                map[string]int
}

func (m *migrator) report(name string) *report {
	r := &report{name: name, reasons: map[string]int{}}
	m.reports = append(m.reports, r)
	return r
}

func (r *report) skip(reason string) {
	r.skipped++
	r.reasons[reason]++
}

func (m *migrator) printReport() {
	fmt.Printf("\n%-24s %7s %7s %7s\n", "PASS", "READ", "WRITTEN", "SKIPPED")
	fmt.Println(strings.Repeat("-", 50))
	for _, r := range m.reports {
		fmt.Printf("%-24s %7d %7d %7d\n", r.name, r.read, r.written, r.skipped)
		keys := make([]string, 0, len(r.reasons))
		for k := range r.reasons {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    %-40s %5d\n", k, r.reasons[k])
		}
	}
}

func (m *migrator) ensureScratch() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS migration_id_map (
			entity text NOT NULL, legacy_id text NOT NULL, new_id text NOT NULL,
			PRIMARY KEY (entity, legacy_id))`,
		`CREATE TABLE IF NOT EXISTS legacy_highlight (
			id bigint PRIMARY KEY, content text, attachments text, author_id bigint,
			timestamp timestamp, deleted boolean, msg_id bigint, channel_id bigint,
			server_id bigint, highlight_msg_id bigint, created_at timestamp, updated_at timestamp)`,
		`CREATE TABLE IF NOT EXISTS legacy_issue (
			id bigint PRIMARY KEY, content text, category int, status text,
			user_id bigint, server_id bigint, created_at timestamp, updated_at timestamp)`,
		`CREATE TABLE IF NOT EXISTS legacy_trophy (
			id bigint PRIMARY KEY, reason text, duration int, category int,
			discord_uid bigint, server_id bigint, role_id bigint, expired boolean,
			created_at timestamp, updated_at timestamp)`,
	}
	for _, s := range stmts {
		if _, err := m.tx.Exec(m.ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (m *migrator) loadIDMap() error {
	rows, err := m.tx.Query(m.ctx, `SELECT entity, legacy_id, new_id FROM migration_id_map`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entity, legacy, newID string
		if err := rows.Scan(&entity, &legacy, &newID); err != nil {
			return err
		}
		if m.idMap[entity] == nil {
			m.idMap[entity] = map[string]string{}
		}
		m.idMap[entity][legacy] = newID
	}
	return rows.Err()
}

func (m *migrator) mapped(entity, legacy string) (string, bool) {
	v, ok := m.idMap[entity][legacy]
	return v, ok
}

func (m *migrator) remember(entity, legacy, newID string) error {
	if m.idMap[entity] == nil {
		m.idMap[entity] = map[string]string{}
	}
	m.idMap[entity][legacy] = newID
	_, err := m.tx.Exec(m.ctx,
		`INSERT INTO migration_id_map (entity, legacy_id, new_id) VALUES ($1, $2, $3)
		 ON CONFLICT (entity, legacy_id) DO UPDATE SET new_id = EXCLUDED.new_id`,
		entity, legacy, newID)
	return err
}

func (m *migrator) rows(table string) [][]value {
	return m.tables[table]
}

func (m *migrator) passInstances() error {
	r := m.report("1 instances")

	guilds := map[string]bool{}
	for _, s := range m.servers {
		guilds[strconv.FormatInt(s.ID, 10)] = true
	}
	for table, idx := range map[string]int{"triggers": 5, "highlights": 8, "issues": 5, "trophies": 5} {
		for _, row := range m.rows(table) {
			if !row[idx].null {
				guilds[row[idx].s] = true
			}
		}
	}

	defaults := map[string]string{}
	for _, s := range m.servers {
		if s.DefaultChannel != 0 {
			defaults[strconv.FormatInt(s.ID, 10)] = strconv.FormatInt(s.DefaultChannel, 10)
		}
	}

	names := make([]string, 0, len(guilds))
	for g := range guilds {
		names = append(names, g)
	}
	sort.Strings(names)

	for _, guild := range names {
		r.read++
		id, err := m.upsertInstance(guild, defaults[guild])
		if err != nil {
			return err
		}
		m.instances[guild] = id
		r.written++
	}

	id, err := m.upsertInstance(fallbackInstanceUID, "")
	if err != nil {
		return err
	}
	m.fallback = id
	return nil
}

func (m *migrator) upsertInstance(uid, defaultChannel string) (int64, error) {
	meta := callermeta.Origin{InstanceUID: uid}.InstanceMeta()
	var channel *string
	if defaultChannel != "" {
		channel = &defaultChannel
	}
	var id int64
	err := m.tx.QueryRow(m.ctx,
		`INSERT INTO instance (platform_enum, instance_meta, default_channel)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (platform_enum, instance_meta)
		     DO UPDATE SET default_channel = COALESCE(EXCLUDED.default_channel, instance.default_channel)
		 RETURNING id`,
		discordPlatform, meta, channel).Scan(&id)
	return id, err
}

func (m *migrator) passUsers() error {
	r := m.report("2 users")

	for _, row := range m.rows("users") {
		r.read++
		legacy := row[0].s

		if id, ok := m.mapped("user", legacy); ok {
			m.users[legacy] = id
			r.skip("already migrated")
			continue
		}

		id, err := uuid.NewV7()
		if err != nil {
			return err
		}

		clearance, exact := clearanceFor(int(atoi(row[6])))
		if !exact {
			r.reasons["clearance banded down (inexact legacy level)"]++
		}

		birthday, ok := parseBirthday(row[5])
		if !row[5].null && !ok {
			r.reasons["birthday unparseable, set NULL"]++
		}

		var birthdayArg, congratsArg *time.Time
		if ok {
			birthdayArg = &birthday
		}
		if year := int(atoi(row[9])); year > 0 {
			c := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			if ok {
				c = time.Date(year, birthday.Month(), birthday.Day(),
					birthday.Hour(), birthday.Minute(), birthday.Second(), 0, time.UTC)
			}
			congratsArg = &c
		}

		if _, err := m.tx.Exec(m.ctx,
			`INSERT INTO user_account
			   (id, username, clearance, avatar, locale, timezone, birthday, last_congratulated_at, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id.String(), row[1].s, clearance, nullable(row[3]), nullable(row[4]), nullable(row[10]),
			birthdayArg, congratsArg, mustTime(row[7]), mustTime(row[8])); err != nil {
			return err
		}
		if err := m.remember("user", legacy, id.String()); err != nil {
			return err
		}
		m.users[legacy] = id.String()
		r.written++
	}
	return nil
}

// clearanceFor bands a legacy 0-1000 level down; the second result reports
// whether it was an exact band.
func clearanceFor(level int) (int32, bool) {
	bands := []struct {
		level     int
		clearance int32
	}{
		{1000, int32(pb.Clearance_CLEARANCE_OWNER.Number())},
		{750, int32(pb.Clearance_CLEARANCE_ADMINISTRATOR.Number())},
		{500, int32(pb.Clearance_CLEARANCE_MODERATOR.Number())},
		{250, int32(pb.Clearance_CLEARANCE_MODERATOR.Number())},
		{100, int32(pb.Clearance_CLEARANCE_MEMBER.Number())},
		{0, int32(pb.Clearance_CLEARANCE_REGISTERED.Number())},
	}
	for _, b := range bands {
		if level >= b.level {
			return b.clearance, level == b.level
		}
	}
	return int32(pb.Clearance_CLEARANCE_REGISTERED.Number()), false
}

var birthdayLayouts = []string{
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006/01/02",
	"2006.01.02",
	"02-01-2006",
	"02/01/2006",
	"02.01.2006",
}

func parseBirthday(v value) (time.Time, bool) {
	if v.null || strings.TrimSpace(v.s) == "" {
		return time.Time{}, false
	}
	s := strings.TrimSpace(v.s)
	for _, layout := range birthdayLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func (m *migrator) passPlatformUsers() error {
	r := m.report("3 platform identities")

	for _, row := range m.rows("authorizations") {
		r.read++
		provider, legacyUser, uid := row[1].s, row[3].s, row[2].s

		if provider != "discord" {
			r.skip("unsupported provider " + provider)
			continue
		}
		userID, ok := m.users[legacyUser]
		if !ok {
			r.skip("orphaned user_id (no such user)")
			continue
		}
		m.platformUID[legacyUser] = uid

		if _, done := m.mapped("platform_user", row[0].s); done {
			r.skip("already migrated")
			continue
		}

		var id int64
		err := m.tx.QueryRow(m.ctx,
			`INSERT INTO platform_user (user_id, platform_enum, platform_uid, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (platform_enum, platform_uid)
			     DO UPDATE SET user_id = platform_user.user_id
			 RETURNING id`,
			userID, discordPlatform, uid, mustTime(row[4]), mustTime(row[5])).Scan(&id)
		if err != nil {
			return err
		}
		if err := m.remember("platform_user", row[0].s, strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		r.written++
	}
	return nil
}

func (m *migrator) passDestinations() error {
	r := m.report("4 destinations")

	// highlights is the only place the old schema records a channel's guild.
	channelGuild := map[string]string{}
	for _, s := range m.servers {
		guild := strconv.FormatInt(s.ID, 10)
		for _, c := range []int64{s.DefaultChannel, s.HighlightChannel} {
			if c != 0 {
				channelGuild[strconv.FormatInt(c, 10)] = guild
			}
		}
	}
	for _, row := range m.rows("highlights") {
		if !row[7].null && !row[8].null {
			channelGuild[row[7].s] = row[8].s
		}
	}

	type want struct {
		channel string
		dm      bool
		user    string
	}
	seen := map[string]want{}
	for _, row := range m.rows("reminders") {
		if row[4].null || row[4].s == "0" {
			key := "dm:" + m.platformUID[row[3].s]
			seen[key] = want{dm: true, user: row[3].s}
			continue
		}
		seen[row[4].s] = want{channel: row[4].s}
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		r.read++
		w := seen[key]

		instanceID := m.fallback
		uid := key
		switch {
		case w.dm:
			r.reasons["DM destination (no guild)"]++
		default:
			if guild, ok := channelGuild[w.channel]; ok {
				if id, ok := m.instances[guild]; ok {
					instanceID = id
				}
			} else {
				r.reasons["channel unresolved, attached to fallback instance"]++
			}
		}

		id, err := m.upsertDestination(instanceID, uid)
		if err != nil {
			return err
		}
		m.channels[key] = id
		r.written++
	}
	return nil
}

func (m *migrator) upsertDestination(instanceID int64, uid string) (int64, error) {
	meta := callermeta.Origin{DestinationUID: uid}.DestinationMeta()
	var id int64
	err := m.tx.QueryRow(m.ctx,
		`INSERT INTO destination (instance_id, destination_meta)
		 VALUES ($1, $2)
		 ON CONFLICT (instance_id, destination_meta)
		     DO UPDATE SET instance_id = destination.instance_id
		 RETURNING id`,
		instanceID, meta).Scan(&id)
	return id, err
}

func (m *migrator) passFiles() error {
	r := m.report("5 files")

	names := map[string]bool{}
	for _, row := range m.rows("triggers") {
		if !row[3].null && row[3].s != "" {
			names[row[3].s] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	for _, name := range sorted {
		r.read++

		if id, ok := m.mapped("file", name); ok {
			m.files[name] = id
			r.skip("already migrated")
			continue
		}

		content, err := os.ReadFile(filepath.Join(m.dataDir, "triggers", name))
		if err != nil {
			// Expected: the old bot GC'd orphans out of data/triggers.
			r.skip("blob missing on disk")
			continue
		}

		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		key := triggerFileKeyPrefix + hash[:2] + "/" + hash

		var (
			id       string
			inserted bool
		)
		fileUUID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		err = m.tx.QueryRow(m.ctx,
			`INSERT INTO file (id, category, path, mime_type, byte_size, file_hash)
			 VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (file_hash) WHERE deleted = FALSE
			     DO UPDATE SET file_hash = file.file_hash
			 RETURNING id, (xmax = 0)`,
			fileUUID.String(), db.FileCategoryLocal, key,
			http.DetectContentType(content), int32(len(content)), hash).Scan(&id, &inserted)
		if err != nil {
			return err
		}

		if inserted && !m.dryRun {
			if _, err := m.blobs.Put(m.ctx, key, bytes.NewReader(content)); err != nil {
				return err
			}
		}
		if !inserted {
			r.reasons["deduped onto an identical existing blob"]++
		}
		if err := m.remember("file", name, id); err != nil {
			return err
		}
		m.files[name] = id
		r.written++
	}
	return nil
}

func (m *migrator) passTriggers() error {
	r := m.report("6 triggers")

	for _, row := range m.rows("triggers") {
		r.read++
		legacy := row[0].s

		if id, ok := m.mapped("trigger", legacy); ok {
			m.triggers[legacy] = id
			r.skip("already migrated")
			continue
		}

		userID, ok := m.users[row[4].s]
		if !ok {
			r.skip("orphaned user_id (no such user)")
			continue
		}
		instanceID, ok := m.instances[row[5].s]
		if !ok {
			r.skip("unknown server_id")
			continue
		}

		mode := legacyTriggerMode(int(atoi(row[7])))
		phrase := row[1].s
		if mode == pb.TriggerMode_TRIGGER_MODE_REGEX {
			phrase = stripRegexDelimiters(phrase)
		}
		if _, err := trigger.Compile(phrase, mode); err != nil {
			r.skip("phrase does not compile under RE2")
			continue
		}

		reply := nullable(row[2])
		var fileID *string
		if !row[3].null && row[3].s != "" {
			if id, ok := m.files[row[3].s]; ok {
				fileID = &id
			}
		}
		if reply == nil && fileID == nil {
			// Not recorded in migration_id_map, so a later run retries it.
			r.skip("no reply and no available file")
			continue
		}

		id, err := uuid.NewV7()
		if err != nil {
			return err
		}

		var inserted string
		err = m.tx.QueryRow(m.ctx,
			`INSERT INTO trigger (id, phrase, reply, file_id, user_id, chance, mode, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 ON CONFLICT (lower(phrase)) WHERE mode = 1 AND deleted = FALSE
			     DO NOTHING
			 RETURNING id`,
			id.String(), phrase, reply, fileID, userID,
			int32(atoi(row[6])), int32(mode.Number()),
			mustTime(row[11]), mustTime(row[12])).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			r.skip("duplicate exact-mode phrase")
			continue
		}
		if err != nil {
			return err
		}

		if _, err := m.tx.Exec(m.ctx,
			`INSERT INTO trigger_instance (trigger_id, instance_id) VALUES ($1,$2)
			 ON CONFLICT DO NOTHING`, inserted, instanceID); err != nil {
			return err
		}
		if err := m.remember("trigger", legacy, inserted); err != nil {
			return err
		}
		m.triggers[legacy] = inserted
		r.written++
	}
	return nil
}

// legacyTriggerMode maps the old 0=exact/1=any/2=regex onto the new enum.
func legacyTriggerMode(mode int) pb.TriggerMode {
	switch mode {
	case 0:
		return pb.TriggerMode_TRIGGER_MODE_EXACT
	case 2:
		return pb.TriggerMode_TRIGGER_MODE_REGEX
	default:
		return pb.TriggerMode_TRIGGER_MODE_ANY
	}
}

// Unwraps a Ruby /pattern/flags literal; BuildPattern already applies (?i).
func stripRegexDelimiters(phrase string) string {
	if len(phrase) < 2 || phrase[0] != '/' {
		return phrase
	}
	end := strings.LastIndexByte(phrase, '/')
	if end <= 0 {
		return phrase
	}
	if strings.Trim(phrase[end+1:], "i") != "" {
		return phrase
	}
	return phrase[1:end]
}

// ListTriggerStats uses COUNT(*), so one action_record row per event is needed.
func (m *migrator) passTriggerStats() error {
	r := m.report("7 trigger stats")

	type rec struct {
		actionType int32
		ts         time.Time
		actor      string
		subject    string
	}
	var recs []rec

	for _, row := range m.rows("triggers") {
		r.read++
		triggerID, ok := m.triggers[row[0].s]
		if !ok {
			r.skip("trigger not migrated")
			continue
		}
		if _, done := m.mapped("trigger_stats", row[0].s); done {
			r.skip("already migrated")
			continue
		}

		when := mustTime(row[12])
		if !row[10].null {
			when = mustTime(row[10])
		}
		actor := m.users[row[4].s]

		for _, c := range []struct {
			count      int64
			actionType pb.ActionType
		}{
			{atoi(row[8]), pb.ActionType_ACTION_TYPE_TRIGGER_OCCURRED},
			{atoi(row[9]), pb.ActionType_ACTION_TYPE_TRIGGER_CALLED},
		} {
			for i := int64(0); i < c.count; i++ {
				recs = append(recs, rec{int32(c.actionType.Number()), when, actor, triggerID})
			}
		}
		if err := m.remember("trigger_stats", row[0].s, triggerID); err != nil {
			return err
		}
		r.written++
	}

	if len(recs) == 0 {
		return nil
	}
	_, err := m.tx.CopyFrom(m.ctx,
		pgx.Identifier{"action_record"},
		[]string{"action_type", "action_timestamp", "actor_id", "subject_id"},
		pgx.CopyFromSlice(len(recs), func(i int) ([]any, error) {
			var actor any
			if recs[i].actor != "" {
				actor = recs[i].actor
			}
			return []any{recs[i].actionType, recs[i].ts, actor, recs[i].subject}, nil
		}))
	if err != nil {
		return err
	}
	r.reasons[fmt.Sprintf("action_record rows seeded: %d", len(recs))]++
	return nil
}

func (m *migrator) passReminders() error {
	r := m.report("8 reminders")

	for _, row := range m.rows("reminders") {
		r.read++
		legacy := row[0].s

		if id, ok := m.mapped("reminder", legacy); ok {
			m.reminders[legacy] = id
			r.skip("already migrated")
			continue
		}

		userID, ok := m.users[row[3].s]
		if !ok {
			r.skip("orphaned user_id (no such user)")
			continue
		}

		key := row[4].s
		if row[4].null || row[4].s == "0" {
			key = "dm:" + m.platformUID[row[3].s]
		}
		destinationID, ok := m.channels[key]
		if !ok {
			r.skip("destination unresolved")
			continue
		}

		// H4: reminder.timezone is NOT NULL, so it must always resolve.
		tz := "UTC"
		if !row[9].null && row[9].s != "" {
			tz = row[9].s
		} else if u := m.legacyUserTimezone(row[3].s); u != "" {
			tz = u
			r.reasons["timezone inherited from the user"]++
		} else {
			r.reasons["timezone defaulted to UTC"]++
		}

		var cron *string
		if secs := atoi(row[5]); secs > 0 {
			// @every expresses any fixed interval, so no precision is lost.
			s := "@every " + (time.Duration(secs) * time.Second).String()
			cron = &s
		}

		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err := m.tx.Exec(m.ctx,
			`INSERT INTO reminder
			   (id, datetime, timezone, repeat_cron, destination_id, status, user_id, message, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id.String(), mustTime(row[1]), tz, cron, destinationID,
			int32(pb.ReminderStatus_REMINDER_STATUS_PENDING.Number()),
			userID, nullable(row[2]), mustTime(row[7]), mustTime(row[8])); err != nil {
			return err
		}
		if err := m.remember("reminder", legacy, id.String()); err != nil {
			return err
		}
		m.reminders[legacy] = id.String()
		r.written++
	}

	// Parents are linked second so a child inserted before its parent still resolves.
	for _, row := range m.rows("reminders") {
		if row[6].null {
			continue
		}
		child, ok := m.reminders[row[0].s]
		if !ok {
			continue
		}
		parent, ok := m.reminders[row[6].s]
		if !ok {
			r.reasons["dangling parent, set NULL"]++
			continue
		}
		if _, err := m.tx.Exec(m.ctx,
			`UPDATE reminder SET parent_id = $1 WHERE id = $2`, parent, child); err != nil {
			return err
		}
	}
	return nil
}

func (m *migrator) legacyUserTimezone(legacyUserID string) string {
	for _, row := range m.rows("users") {
		if row[0].s == legacyUserID && !row[10].null {
			return row[10].s
		}
	}
	return ""
}

// passStaging copies deferred features verbatim, inventing no schema for them.
func (m *migrator) passStaging() error {
	specs := []struct {
		legacy, target string
		columns        []string
		bools          map[int]bool
	}{
		{"highlights", "legacy_highlight",
			[]string{"id", "content", "attachments", "author_id", "timestamp", "deleted",
				"msg_id", "channel_id", "server_id", "highlight_msg_id", "created_at", "updated_at"},
			map[int]bool{5: true}},
		{"issues", "legacy_issue",
			[]string{"id", "content", "category", "status", "user_id", "server_id", "created_at", "updated_at"},
			nil},
		{"trophies", "legacy_trophy",
			[]string{"reason", "duration", "category", "discord_uid", "server_id", "role_id", "expired",
				"created_at", "updated_at"},
			map[int]bool{7: true}},
	}
	specs[2].columns = append([]string{"id"}, specs[2].columns...)

	for _, spec := range specs {
		r := m.report("9 " + spec.target)
		for _, row := range m.rows(spec.legacy) {
			r.read++
			if len(row) < len(spec.columns) {
				return fmt.Errorf("%s: expected %d columns, got %d", spec.legacy, len(spec.columns), len(row))
			}

			args := make([]any, len(spec.columns))
			placeholders := make([]string, len(spec.columns))
			for i := range spec.columns {
				placeholders[i] = fmt.Sprintf("$%d", i+1)
				switch {
				case row[i].null:
					args[i] = nil
				case spec.bools[i]:
					args[i] = row[i].s != "0"
				case strings.Contains(spec.columns[i], "_at") || spec.columns[i] == "timestamp":
					args[i] = mustTime(row[i])
				default:
					args[i] = row[i].s
				}
			}

			tag, err := m.tx.Exec(m.ctx, fmt.Sprintf(
				`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (id) DO NOTHING`,
				spec.target, strings.Join(spec.columns, ", "), strings.Join(placeholders, ", ")), args...)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				r.skip("already staged")
				continue
			}
			r.written++
		}
	}
	return nil
}

func nullable(v value) *string {
	if v.null || v.s == "" {
		return nil
	}
	return &v.s
}

func atoi(v value) int64 {
	if v.null {
		return 0
	}
	n, err := strconv.ParseInt(v.s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

var timeLayouts = []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05"}

func mustTime(v value) time.Time {
	if v.null {
		return time.Time{}
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, v.s); err == nil {
			return t
		}
	}
	return time.Time{}
}

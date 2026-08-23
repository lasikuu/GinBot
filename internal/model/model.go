// Package model holds plain Go structs mirroring database rows.
//
// These exist because the generated protobuf types use the opaque API
// (features.(pb.go).api_level = API_OPAQUE), which has no exported fields and
// therefore cannot be used as a pgx scan target. Scanning into a *pb.User and
// friends silently fails at runtime.
//
// The database layer scans into these structs; callers convert to protobuf at
// the service boundary via the ToProto methods.
package model

import (
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestamp converts a nullable time column into a protobuf timestamp.
// A nil input yields a nil timestamp, which the opaque builders treat as unset.
func timestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// Note: ToProto assigns message-typed fields (InstanceMeta, Destination) by
// pointer, so the returned protobuf shares that state with the row struct.
// Callers must not mutate either afterwards; clone first if that is ever needed.

// User mirrors a row of user_account.
type User struct {
	ID                  string
	Username            string
	Clearance           int32
	Avatar              *string
	Locale              *string
	Timezone            *string
	Birthday            *time.Time
	LastCongratulatedAt *time.Time
	Deleted             bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// UserColumns lists user_account columns in the order ScanUser expects.
const UserColumns = `id, username, clearance, avatar, locale, timezone,
	birthday, last_congratulated_at, deleted, created_at, updated_at`

// ScanTargets returns pointers to every field, in UserColumns order.
func (u *User) ScanTargets() []any {
	return []any{
		&u.ID, &u.Username, &u.Clearance, &u.Avatar, &u.Locale, &u.Timezone,
		&u.Birthday, &u.LastCongratulatedAt, &u.Deleted, &u.CreatedAt, &u.UpdatedAt,
	}
}

func (u *User) ToProto() *pb.User {
	clearance := pb.Clearance(u.Clearance)
	return pb.User_builder{
		Id:                  &u.ID,
		Username:            &u.Username,
		Clearance:           &clearance,
		Avatar:              u.Avatar,
		Locale:              u.Locale,
		Timezone:            u.Timezone,
		Birthday:            timestamp(u.Birthday),
		LastCongratulatedAt: timestamp(u.LastCongratulatedAt),
		CreatedAt:           timestamppb.New(u.CreatedAt),
		UpdatedAt:           timestamppb.New(u.UpdatedAt),
	}.Build()
}

// Instance mirrors a row of instance.
type Instance struct {
	ID             int64
	PlatformEnum   int32
	InstanceMeta   *structpb.Struct
	DefaultChannel *string
	Deleted        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// InstanceColumns lists instance columns in the order ScanTargets expects.
const InstanceColumns = `id, platform_enum, instance_meta, default_channel, deleted, created_at, updated_at`

func (i *Instance) ScanTargets() []any {
	return []any{
		&i.ID, &i.PlatformEnum, &i.InstanceMeta, &i.DefaultChannel,
		&i.Deleted, &i.CreatedAt, &i.UpdatedAt,
	}
}

func (i *Instance) ToProto() *pb.Instance {
	platform := pb.Platform(i.PlatformEnum)
	return pb.Instance_builder{
		Id:             &i.ID,
		PlatformEnum:   &platform,
		InstanceMeta:   i.InstanceMeta,
		DefaultChannel: i.DefaultChannel,
		CreatedAt:      timestamppb.New(i.CreatedAt),
		UpdatedAt:      timestamppb.New(i.UpdatedAt),
	}.Build()
}

// Reminder mirrors a row of reminder.
type Reminder struct {
	ID            string
	Datetime      time.Time
	Timezone      string
	RepeatCron    *string
	DestinationID int64
	Status        int32
	UserID        *string
	Message       *string
	ParentID      *string
	Deleted       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// ClaimedAt is the absolute instant the delivery loop claimed this reminder,
	// and is NULL in every status other than SENT. It is the reclaim's clock;
	// updated_at is deliberately not used for that, because it is a
	// `timestamp without time zone` written through a session-timezone-dependent
	// cast. There is no protobuf counterpart: it is an internal scheduling
	// detail, not something a client has any use for.
	ClaimedAt *time.Time
	// DeliveryAttempts counts claims that never got a confirmation to stick, and
	// bounds retrying a delivery whose confirm is rejected permanently rather
	// than merely lost. Also deliberately absent from the protobuf.
	DeliveryAttempts int32
}

// ReminderColumns lists reminder columns in the order ScanTargets expects.
const ReminderColumns = `id, datetime, timezone, repeat_cron, destination_id, status,
	user_id, message, parent_id, deleted, created_at, updated_at,
	claimed_at, delivery_attempts`

func (r *Reminder) ScanTargets() []any {
	return []any{
		&r.ID, &r.Datetime, &r.Timezone, &r.RepeatCron, &r.DestinationID, &r.Status,
		&r.UserID, &r.Message, &r.ParentID, &r.Deleted, &r.CreatedAt, &r.UpdatedAt,
		&r.ClaimedAt, &r.DeliveryAttempts,
	}
}

// ToProto converts the row to its protobuf representation.
//
// destination is looked up separately by the caller when the full
// ReminderDestination is required; it is optional here.
func (r *Reminder) ToProto(destination *pb.ReminderDestination) *pb.Reminder {
	status := pb.ReminderStatus(r.Status)
	return pb.Reminder_builder{
		Id:          &r.ID,
		Datetime:    timestamppb.New(r.Datetime),
		Timezone:    &r.Timezone,
		RepeatCron:  r.RepeatCron,
		Destination: destination,
		Status:      &status,
		UserId:      r.UserID,
		Message:     r.Message,
		ParentId:    r.ParentID,
		CreatedAt:   timestamppb.New(r.CreatedAt),
		UpdatedAt:   timestamppb.New(r.UpdatedAt),
	}.Build()
}

// Trigger mirrors a row of trigger.
type Trigger struct {
	ID        string
	Phrase    string
	Reply     *string
	FileID    *string
	UserID    *string
	Chance    int32
	Mode      int32
	Deleted   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TriggerColumns lists trigger columns in the order ScanTargets expects.
const TriggerColumns = `id, phrase, reply, file_id, user_id, chance, mode,
	deleted, created_at, updated_at`

// ScanTargets returns pointers to every field, in TriggerColumns order.
func (t *Trigger) ScanTargets() []any {
	return []any{
		&t.ID, &t.Phrase, &t.Reply, &t.FileID, &t.UserID, &t.Chance, &t.Mode,
		&t.Deleted, &t.CreatedAt, &t.UpdatedAt,
	}
}

// ToProto converts the row to its protobuf representation.
//
// file is looked up separately by the caller when the trigger has one; it is
// optional here. instances likewise.
func (t *Trigger) ToProto(file *pb.TriggerFile, instances []*pb.TriggerInstance) *pb.Trigger {
	mode := pb.TriggerMode(t.Mode)
	return pb.Trigger_builder{
		Id:        &t.ID,
		Phrase:    &t.Phrase,
		Reply:     t.Reply,
		UserId:    t.UserID,
		Instances: instances,
		Chance:    &t.Chance,
		CreatedAt: timestamppb.New(t.CreatedAt),
		UpdatedAt: timestamppb.New(t.UpdatedAt),
		Mode:      &mode,
		File:      file,
	}.Build()
}

// File mirrors a row of file.
type File struct {
	ID        string
	Category  int32
	Path      string
	MimeType  string
	ByteSize  int32
	FileHash  string
	Deleted   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// FileColumns lists file columns in the order ScanTargets expects.
const FileColumns = `id, category, path, mime_type, byte_size, file_hash,
	deleted, created_at, updated_at`

// ScanTargets returns pointers to every field, in FileColumns order.
func (f *File) ScanTargets() []any {
	return []any{
		&f.ID, &f.Category, &f.Path, &f.MimeType, &f.ByteSize, &f.FileHash,
		&f.Deleted, &f.CreatedAt, &f.UpdatedAt,
	}
}

// ToProto converts the row to its protobuf representation. filename is not a
// column: the original name is not stored, so the caller supplies what it wants
// the attachment to be called.
func (f *File) ToProto(filename string) *pb.TriggerFile {
	byteSize := int64(f.ByteSize)
	return pb.TriggerFile_builder{
		FileId:   &f.ID,
		Filename: &filename,
		MimeType: &f.MimeType,
		ByteSize: &byteSize,
	}.Build()
}

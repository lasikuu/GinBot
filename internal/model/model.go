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

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
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

// ToProto converts the row to its protobuf representation.
//
// It shares no message-typed state with the row: every timestamp is freshly
// allocated by timestamppb.New and the rest are scalars. Unlike Instance,
// Reminder and Trigger, this one carries no aliasing contract — the only
// pointers it retains are into the row's own string fields, which nothing
// reassigns after conversion.
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

// ToProto converts the row to its protobuf representation.
//
// SHARED BY POINTER: the returned Instance's instance_meta IS i.InstanceMeta,
// the same *structpb.Struct, not a copy. Nothing is cloned, so neither the row
// nor the returned message may be mutated after this call; clone first if that
// is ever needed.
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
//
// SHARED BY POINTER: the returned Reminder's destination IS the destination
// argument, not a copy. Nothing is cloned, so neither the argument nor the
// returned message may be mutated after this call; clone first if that is ever
// needed. The list path (ListRemindersByUser -> ListReminders) builds a fresh
// ReminderDestination per row, so no caller currently reuses one across
// conversions.
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
//
// SHARED BY POINTER: the returned Trigger's file IS the file argument and its
// instances IS the instances slice — same backing array, same elements — not
// copies. Nothing is cloned, so neither the arguments nor the returned message
// may be mutated after this call; clone first if that is ever needed.
//
// This is documented rather than defended against because no call site
// currently exhibits it: ListTriggers, the only path that converts many rows in
// a loop, builds a fresh file and a fresh instances slice per iteration. Cloning
// every row on the list paths would allocate on every listing to guard a latent
// footgun rather than a live bug, and the aliasing is confined to this single
// conversion at the service boundary.
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

// The jsonb field names inside repost_entry.msg_ref. They are a storage
// contract: renaming one orphans every stored reference, since existing rows'
// jsonb bodies were written under the old names and nothing rewrites them.
const (
	RefFieldInstanceUID    = "instance_uid"
	RefFieldDestinationUID = "destination_uid"
	RefFieldMessageUID     = "message_uid"
	RefFieldAuthorUID      = "author_uid"
)

// RepostEntry mirrors a row of repost_entry.
type RepostEntry struct {
	ID            int64
	InstanceID    int64
	DestinationID *int64
	UserID        *string
	Kind          int32
	SourceKey     *string
	CanonicalURL  *string
	FileID        *string
	ContentHash   []byte
	MsgRef        *structpb.Struct
	PostedAt      time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// RepostEntryColumns lists repost_entry columns in ScanTargets order.
const RepostEntryColumns = `id, instance_id, destination_id, user_id, kind,
	source_key, canonical_url, file_id, content_hash, msg_ref, posted_at,
	created_at, updated_at`

// ScanTargets returns pointers to every field, in RepostEntryColumns order.
func (r *RepostEntry) ScanTargets() []any {
	return []any{
		&r.ID, &r.InstanceID, &r.DestinationID, &r.UserID, &r.Kind,
		&r.SourceKey, &r.CanonicalURL, &r.FileID, &r.ContentHash, &r.MsgRef, &r.PostedAt,
		&r.CreatedAt, &r.UpdatedAt,
	}
}

// MessageRef decodes msg_ref into the protobuf reference clients deep-link
// with. A missing or malformed field yields an empty string rather than an
// error: a deep link that cannot be built is a degraded notification, not a
// failure, and RepostMatch.original_ref documents that any field may be
// empty for an entry stored by a platform that does not carry it.
func (r *RepostEntry) MessageRef() *pb.MessageRef {
	instanceUID := r.MsgRef.GetFields()[RefFieldInstanceUID].GetStringValue()
	destinationUID := r.MsgRef.GetFields()[RefFieldDestinationUID].GetStringValue()
	messageUID := r.MsgRef.GetFields()[RefFieldMessageUID].GetStringValue()
	authorUID := r.MsgRef.GetFields()[RefFieldAuthorUID].GetStringValue()

	return pb.MessageRef_builder{
		InstanceUid:    &instanceUID,
		DestinationUid: &destinationUID,
		MessageUid:     &messageUID,
		AuthorUid:      &authorUID,
	}.Build()
}

// NewRepostMsgRef builds the msg_ref jsonb for a new entry.
func NewRepostMsgRef(instanceUID, destinationUID, messageUID, authorUID string) *structpb.Struct {
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			RefFieldInstanceUID:    structpb.NewStringValue(instanceUID),
			RefFieldDestinationUID: structpb.NewStringValue(destinationUID),
			RefFieldMessageUID:     structpb.NewStringValue(messageUID),
			RefFieldAuthorUID:      structpb.NewStringValue(authorUID),
		},
	}
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
//
// Like User.ToProto, it shares no message-typed state with the row — every
// field is a scalar — so it carries no aliasing contract.
func (f *File) ToProto(filename string) *pb.TriggerFile {
	byteSize := int64(f.ByteSize)
	return pb.TriggerFile_builder{
		FileId:   &f.ID,
		Filename: &filename,
		MimeType: &f.MimeType,
		ByteSize: &byteSize,
	}.Build()
}

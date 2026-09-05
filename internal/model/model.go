// Package model mirrors database rows: the opaque-API protobuf types have no
// exported fields and cannot be pgx scan targets.
package model

import (
	"time"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestamp returns nil for a nil input, which the builders treat as unset.
func timestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

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

// UserColumns lists user_account columns in ScanTargets order.
const UserColumns = `id, username, clearance, avatar, locale, timezone,
	birthday, last_congratulated_at, deleted, created_at, updated_at`

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

type Instance struct {
	ID             int64
	PlatformEnum   int32
	InstanceMeta   *structpb.Struct
	DefaultChannel *string
	Deleted        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// InstanceColumns lists instance columns in ScanTargets order.
const InstanceColumns = `id, platform_enum, instance_meta, default_channel, deleted, created_at, updated_at`

func (i *Instance) ScanTargets() []any {
	return []any{
		&i.ID, &i.PlatformEnum, &i.InstanceMeta, &i.DefaultChannel,
		&i.Deleted, &i.CreatedAt, &i.UpdatedAt,
	}
}

// ToProto shares instance_meta by pointer; clone before mutating either side.
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
	// ClaimedAt is the reclaim clock; updated_at cannot serve, being written
	// through a session-timezone-dependent cast.
	ClaimedAt *time.Time
	// DeliveryAttempts bounds retries of a permanently rejected confirm.
	DeliveryAttempts int32
	// Ref is a per-user display alias for ID, never an identity. See ADR-0039.
	Ref int64
}

// ReminderColumns lists reminder columns in ScanTargets order.
const ReminderColumns = `id, datetime, timezone, repeat_cron, destination_id, status,
	user_id, message, parent_id, deleted, created_at, updated_at,
	claimed_at, delivery_attempts, ref`

func (r *Reminder) ScanTargets() []any {
	return []any{
		&r.ID, &r.Datetime, &r.Timezone, &r.RepeatCron, &r.DestinationID, &r.Status,
		&r.UserID, &r.Message, &r.ParentID, &r.Deleted, &r.CreatedAt, &r.UpdatedAt,
		&r.ClaimedAt, &r.DeliveryAttempts, &r.Ref,
	}
}

// ToProto takes an optional destination and shares it by pointer.
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
		Ref:         &r.Ref,
	}.Build()
}

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
	// Ref is a global display alias for ID, never an identity. See ADR-0039.
	Ref int64
}

// TriggerColumns lists trigger columns in ScanTargets order.
const TriggerColumns = `id, phrase, reply, file_id, user_id, chance, mode,
	deleted, created_at, updated_at, ref`

func (t *Trigger) ScanTargets() []any {
	return []any{
		&t.ID, &t.Phrase, &t.Reply, &t.FileID, &t.UserID, &t.Chance, &t.Mode,
		&t.Deleted, &t.CreatedAt, &t.UpdatedAt, &t.Ref,
	}
}

// ToProto takes an optional file and instances and shares both by pointer.
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
		Ref:       &t.Ref,
	}.Build()
}

// Storage contract: renaming one orphans every stored msg_ref reference.
const (
	RefFieldInstanceUID    = "instance_uid"
	RefFieldDestinationUID = "destination_uid"
	RefFieldMessageUID     = "message_uid"
	RefFieldAuthorUID      = "author_uid"
)

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

func (r *RepostEntry) ScanTargets() []any {
	return []any{
		&r.ID, &r.InstanceID, &r.DestinationID, &r.UserID, &r.Kind,
		&r.SourceKey, &r.CanonicalURL, &r.FileID, &r.ContentHash, &r.MsgRef, &r.PostedAt,
		&r.CreatedAt, &r.UpdatedAt,
	}
}

// MessageRef yields an empty string for a missing or malformed field.
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

// FileColumns lists file columns in ScanTargets order.
const FileColumns = `id, category, path, mime_type, byte_size, file_hash,
	deleted, created_at, updated_at`

func (f *File) ScanTargets() []any {
	return []any{
		&f.ID, &f.Category, &f.Path, &f.MimeType, &f.ByteSize, &f.FileHash,
		&f.Deleted, &f.CreatedAt, &f.UpdatedAt,
	}
}

// ToProto takes filename because the original name is not a stored column.
func (f *File) ToProto(filename string) *pb.TriggerFile {
	byteSize := int64(f.ByteSize)
	return pb.TriggerFile_builder{
		FileId:   &f.ID,
		Filename: &filename,
		MimeType: &f.MimeType,
		ByteSize: &byteSize,
	}.Build()
}

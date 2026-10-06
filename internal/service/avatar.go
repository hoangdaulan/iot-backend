package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"iot-backend/internal/apperr"
	"iot-backend/internal/model"
	"iot-backend/internal/repository"
)

const (
	// MaxAvatarBytes is the largest accepted avatar image.
	MaxAvatarBytes = 2 << 20
	// AvatarURLPrefix is where uploaded avatars are served; the stored avatar is this prefix
	// plus the file name.
	AvatarURLPrefix = "/uploads/avatars/"
)

var avatarExtensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// WithUploadDir sets the directory that holds the uploaded files; avatars go in its avatars
// subdirectory.
func (s *AuthService) WithUploadDir(dir string) *AuthService {
	s.uploadDir = dir
	return s
}

// UploadAvatar stores the image as the user's avatar, replacing the previous uploaded one, and
// returns the updated user. The type is detected from the content, not the client's claim.
func (s *AuthService) UploadAvatar(ctx context.Context, userID int64, data []byte) (*model.User, error) {
	if len(data) == 0 {
		return nil, apperr.BadRequest("Avatar file is empty")
	}
	if len(data) > MaxAvatarBytes {
		return nil, apperr.BadRequest("Avatar must be at most 2 MB")
	}
	ext, ok := avatarExtensions[http.DetectContentType(data)]
	if !ok {
		return nil, apperr.BadRequest("Avatar must be a PNG, JPEG, GIF or WebP image")
	}
	user, err := s.Profile(ctx, userID)
	if err != nil {
		return nil, err
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%d-%s%s", userID, hex.EncodeToString(suffix), ext)
	dir := filepath.Join(s.uploadDir, "avatars")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create avatar dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return nil, fmt.Errorf("write avatar: %w", err)
	}

	url := AvatarURLPrefix + name
	updated, err := s.users.UpdateProfile(ctx, userID, repository.ProfileUpdate{Avatar: &url})
	if err != nil {
		_ = os.Remove(filepath.Join(dir, name))
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("User not found")
		}
		return nil, err
	}
	s.removeAvatarFile(user.Avatar)
	return updated, nil
}

// removeAvatarFile deletes the file behind an avatar URL this service issued; other avatars
// (external links) are left alone.
func (s *AuthService) removeAvatarFile(avatar *string) {
	if avatar == nil || !strings.HasPrefix(*avatar, AvatarURLPrefix) {
		return
	}
	name := filepath.Base(strings.TrimPrefix(*avatar, AvatarURLPrefix))
	_ = os.Remove(filepath.Join(s.uploadDir, "avatars", name))
}

package handler

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"iot-backend/internal/apperr"
	"iot-backend/internal/dto"
	"iot-backend/internal/middleware"
	"iot-backend/internal/repository"
	"iot-backend/internal/service"
)

type AuthHandler struct {
	auth   *service.AuthService
	logger *slog.Logger
}

func NewAuthHandler(auth *service.AuthService, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{auth: auth, logger: logger}
}

// Login handles POST /api/auth/login.
// @Summary Log in
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body dto.LoginRequest true "Request body"
// @Success 200 {object} dto.LoginResponse
// @Failure 400 {object} dto.ErrorResponse
// @Failure 401 {object} dto.ErrorResponse
// @Router /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req dto.LoginRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.logger, err)
		return
	}
	token, user, err := h.auth.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, dto.LoginResponse{AccessToken: token, User: dto.UserSummaryFrom(user)})
}

// Register handles POST /api/auth/register.
// @Summary Register a user
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body dto.RegisterRequest true "Request body"
// @Success 201 {object} dto.RegisterResponse
// @Failure 400 {object} dto.ErrorResponse
// @Failure 409 {object} dto.ErrorResponse
// @Router /auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.logger, err)
		return
	}
	user, err := h.auth.Register(c.Request.Context(), service.RegisterInput{
		Name: req.Name, Username: req.Username, Email: req.Email, Password: req.Password,
	})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusCreated, dto.RegisterResponse{
		Message: "Register successfully", User: dto.UserSummaryFrom(user),
	})
}

// Profile handles GET /api/auth/profile.
// @Summary Get current profile
// @Tags Auth
// @Produce json
// @Security BearerAuth
// @Success 200 {object} dto.User
// @Failure 401 {object} dto.ErrorResponse
// @Router /auth/profile [get]
func (h *AuthHandler) Profile(c *gin.Context) {
	user, err := h.auth.Profile(c.Request.Context(), middleware.ClaimsFrom(c).UserID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, dto.UserFrom(user))
}

// UpdateProfile handles PATCH /api/auth/profile.
// @Summary Update current profile
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.UpdateProfileRequest true "Request body"
// @Success 200 {object} dto.User
// @Failure 400 {object} dto.ErrorResponse
// @Failure 401 {object} dto.ErrorResponse
// @Router /auth/profile [patch]
func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	var req dto.UpdateProfileRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.logger, err)
		return
	}
	user, err := h.auth.UpdateProfile(c.Request.Context(), middleware.ClaimsFrom(c).UserID,
		repository.ProfileUpdate{
			Name: req.Name, Phone: req.Phone, Avatar: req.Avatar, Github: req.Github,
			Figma: req.Figma, Swagger: req.Swagger,
		})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, dto.UserFrom(user))
}

// UploadAvatar handles POST /api/auth/avatar (multipart form, field "file").
// @Summary Upload the current user's avatar
// @Description PNG, JPEG, GIF or WebP up to 2 MB. The returned avatar is a path served by this API, e.g. /uploads/avatars/1-ab12.png.
// @Tags Auth
// @Accept mpfd
// @Produce json
// @Security BearerAuth
// @Param file formData file true "Image file"
// @Success 200 {object} dto.User
// @Failure 400 {object} dto.ErrorResponse
// @Failure 401 {object} dto.ErrorResponse
// @Router /auth/avatar [post]
func (h *AuthHandler) UploadAvatar(c *gin.Context) {
	// A little headroom over the image limit for the multipart framing.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxAvatarBytes+(64<<10))
	file, err := c.FormFile("file")
	if err != nil {
		respondError(c, h.logger, apperr.BadRequest("Avatar must be sent as the multipart field \"file\", at most 2 MB"))
		return
	}
	f, err := file.Open()
	if err != nil {
		respondError(c, h.logger, apperr.BadRequest("Invalid avatar file"))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, service.MaxAvatarBytes+1))
	if err != nil {
		respondError(c, h.logger, apperr.BadRequest("Invalid avatar file"))
		return
	}
	user, err := h.auth.UploadAvatar(c.Request.Context(), middleware.ClaimsFrom(c).UserID, data)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, dto.UserFrom(user))
}

// ChangePassword handles PATCH /api/auth/password.
// @Summary Change password
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.ChangePasswordRequest true "Request body"
// @Success 204
// @Failure 400 {object} dto.ErrorResponse
// @Failure 401 {object} dto.ErrorResponse
// @Router /auth/password [patch]
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req dto.ChangePasswordRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.logger, err)
		return
	}
	err := h.auth.ChangePassword(c.Request.Context(), middleware.ClaimsFrom(c).UserID,
		req.OldPassword, req.NewPassword)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.Status(http.StatusNoContent)
}

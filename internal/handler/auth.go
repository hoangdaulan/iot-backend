package handler

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

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
func (h *AuthHandler) Register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.logger, err)
		return
	}
	user, err := h.auth.Register(c.Request.Context(), service.RegisterInput{
		Username: req.Username, Email: req.Email, Password: req.Password,
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
func (h *AuthHandler) Profile(c *gin.Context) {
	user, err := h.auth.Profile(c.Request.Context(), middleware.ClaimsFrom(c).UserID)
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, dto.UserFrom(user))
}

// UpdateProfile handles PATCH /api/auth/profile.
func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	var req dto.UpdateProfileRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.logger, err)
		return
	}
	user, err := h.auth.UpdateProfile(c.Request.Context(), middleware.ClaimsFrom(c).UserID,
		repository.ProfileUpdate{
			Name: req.Name, Phone: req.Phone, Avatar: req.Avatar, Github: req.Github, Figma: req.Figma,
		})
	if err != nil {
		respondError(c, h.logger, err)
		return
	}
	c.JSON(http.StatusOK, dto.UserFrom(user))
}

// ChangePassword handles PATCH /api/auth/password.
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

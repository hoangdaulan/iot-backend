// Package router wires the HTTP routes.
package router

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "iot-backend/docs"
	"iot-backend/internal/dto"
	"iot-backend/internal/handler"
	"iot-backend/internal/middleware"
	"iot-backend/internal/service"
)

type Deps struct {
	Auth               *service.AuthService
	Tokens             *service.TokenService
	Sensors            *service.SensorService
	Devices            *service.DeviceService
	CORSAllowedOrigins []string
	Logger             *slog.Logger
}

func New(d Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.CORS(d.CORSAllowedOrigins))
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, dto.ErrorResponse{Message: "Not found"})
	})
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	auth := handler.NewAuthHandler(d.Auth, d.Logger)
	sensors := handler.NewSensorHandler(d.Sensors, d.Logger)
	devices := handler.NewDeviceHandler(d.Devices, d.Logger)

	api := r.Group("/api")
	api.POST("/auth/login", auth.Login)
	api.POST("/auth/register", auth.Register)

	protected := api.Group("", middleware.Auth(d.Tokens))
	protected.GET("/auth/profile", auth.Profile)
	protected.PATCH("/auth/profile", auth.UpdateProfile)
	protected.PATCH("/auth/password", auth.ChangePassword)

	protected.GET("/sensors", sensors.List)
	protected.GET("/sensor-data/latest", sensors.Latest)
	protected.GET("/sensor-data/history", sensors.History)

	protected.GET("/devices", devices.List)
	protected.POST("/devices/:id/command", devices.Command)
	protected.GET("/devices/control-history", devices.History)

	return r
}

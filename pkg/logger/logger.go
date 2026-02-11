package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger 接口定义了日志记录的基本方法
type Logger interface {
	Debug(msg string, fields ...zap.Field)
	Info(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
	Error(msg string, fields ...zap.Field)
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Sync() error
}

// zapLogger 实现了 Logger 接口
type zapLogger struct {
	logger *zap.Logger
	sugar  *zap.SugaredLogger
}

// NewZapLogger 从 zap.Logger 创建 Logger 实例
func NewZapLogger(zl *zap.Logger) Logger {
	// 添加 CallerSkip(1) 跳过 wrapper 层，显示真实调用位置
	return &zapLogger{
		logger: zl.WithOptions(zap.AddCallerSkip(1)),
		sugar:  zl.WithOptions(zap.AddCallerSkip(1)).Sugar(),
	}
}

func (l *zapLogger) Debug(msg string, fields ...zap.Field) {
	l.logger.Debug(msg, fields...)
}

func (l *zapLogger) Info(msg string, fields ...zap.Field) {
	l.logger.Info(msg, fields...)
}

func (l *zapLogger) Warn(msg string, fields ...zap.Field) {
	l.logger.Warn(msg, fields...)
}

func (l *zapLogger) Error(msg string, fields ...zap.Field) {
	l.logger.Error(msg, fields...)
}

func (l *zapLogger) Debugf(format string, args ...interface{}) {
	l.sugar.Debugf(format, args...)
}

func (l *zapLogger) Infof(format string, args ...interface{}) {
	l.sugar.Infof(format, args...)
}

func (l *zapLogger) Warnf(format string, args ...interface{}) {
	l.sugar.Warnf(format, args...)
}

func (l *zapLogger) Errorf(format string, args ...interface{}) {
	l.sugar.Errorf(format, args...)
}

func (l *zapLogger) Sync() error {
	return l.logger.Sync()
}

// NewDefault 创建默认的 logger（输出到标准输出）
func NewDefault() Logger {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
	core := zapcore.NewCore(
		consoleEncoder,
		zapcore.AddSync(os.Stdout),
		zap.InfoLevel,
	)

	zl := zap.New(core)
	return NewZapLogger(zl)
}

// NewProduction 创建生产环境的 logger（JSON格式，同时输出到文件和控制台）
func NewProduction(logFilePath string, level zapcore.Level) (Logger, error) {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	// 文件输出配置
	fileEncoder := zapcore.NewJSONEncoder(encoderConfig)
	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	// 控制台输出配置
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)

	// 创建多重输出core
	core := zapcore.NewTee(
		zapcore.NewCore(fileEncoder, zapcore.AddSync(logFile), level),
		zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), level),
	)

	zl := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	return NewZapLogger(zl), nil
}


package connection

import (
	"context"
	"net"

	"go.uber.org/zap"
)

// connectionFactory implements ConnectionFactory interface
type connectionFactory struct {
	defaultPolicy   *TimeoutPolicy
	defaultIOConfig *IOWrapperConfig
	logger          *zap.Logger
	metricsReporter MetricsReporter
}

// ConnectionFactoryConfig configures the connection factory
type ConnectionFactoryConfig struct {
	DefaultPolicy   *TimeoutPolicy
	DefaultIOConfig *IOWrapperConfig
	Logger          *zap.Logger
	MetricsReporter MetricsReporter
}

// NewConnectionFactory creates a new connection factory
func NewConnectionFactory(config *ConnectionFactoryConfig) ConnectionFactory {
	factory := &connectionFactory{
		logger: zap.NewNop(),
	}

	if config != nil {
		if config.DefaultPolicy != nil {
			factory.defaultPolicy = config.DefaultPolicy
		}

		if config.DefaultIOConfig != nil {
			factory.defaultIOConfig = config.DefaultIOConfig
		}

		if config.Logger != nil {
			factory.logger = config.Logger
		}

		factory.metricsReporter = config.MetricsReporter
	}

	// Set defaults if not provided
	if factory.defaultPolicy == nil {
		factory.defaultPolicy = DefaultTimeoutPolicy()
	}

	if factory.defaultIOConfig == nil {
		factory.defaultIOConfig = DefaultIOWrapperConfig()
	}

	return factory
}

// CreateConnection creates a new ConnectionManager with default settings
func (cf *connectionFactory) CreateConnection(conn net.Conn, policy *TimeoutPolicy) (ConnectionManager, error) {
	return cf.CreateConnectionWithContext(context.Background(), conn, policy)
}

// CreateConnectionWithContext creates a new ConnectionManager with context
func (cf *connectionFactory) CreateConnectionWithContext(ctx context.Context, conn net.Conn, policy *TimeoutPolicy) (ConnectionManager, error) {
	if policy == nil {
		policy = cf.defaultPolicy
	}

	config := &ConnectionManagerConfig{
		Policy:          policy,
		Logger:          cf.logger,
		MetricsReporter: cf.metricsReporter,
		IOConfig:        cf.defaultIOConfig,
	}

	manager, err := NewConnectionManager(conn, config)
	if err != nil {
		return nil, err
	}

	cf.logger.Debug("Created new connection through factory",
		zap.String("remote_addr", conn.RemoteAddr().String()),
		zap.Duration("handshake_timeout", policy.HandshakeTimeout),
		zap.Duration("operation_max_timeout", policy.OperationMaxTimeout))

	return manager, nil
}

// CreateConnectionWithCustomConfig creates a connection with fully custom configuration
func (cf *connectionFactory) CreateConnectionWithCustomConfig(
	ctx context.Context,
	conn net.Conn,
	config *ConnectionManagerConfig,
) (ConnectionManager, error) {
	if config == nil {
		config = &ConnectionManagerConfig{
			Policy:          cf.defaultPolicy,
			Logger:          cf.logger,
			MetricsReporter: cf.metricsReporter,
			IOConfig:        cf.defaultIOConfig,
		}
	}

	// Fill in defaults for nil fields
	if config.Policy == nil {
		config.Policy = cf.defaultPolicy
	}

	if config.Logger == nil {
		config.Logger = cf.logger
	}

	if config.IOConfig == nil {
		config.IOConfig = cf.defaultIOConfig
	}

	if config.MetricsReporter == nil {
		config.MetricsReporter = cf.metricsReporter
	}

	return NewConnectionManager(conn, config)
}

// GetDefaultPolicy returns the default timeout policy
func (cf *connectionFactory) GetDefaultPolicy() *TimeoutPolicy {
	return cf.defaultPolicy
}

// UpdateDefaultPolicy updates the default timeout policy
func (cf *connectionFactory) UpdateDefaultPolicy(policy *TimeoutPolicy) {
	if policy != nil {
		cf.defaultPolicy = policy
		cf.logger.Info("Updated default timeout policy",
			zap.Duration("handshake_timeout", policy.HandshakeTimeout),
			zap.Duration("operation_max_timeout", policy.OperationMaxTimeout))
	}
}

// CreateTestConnection creates a connection specifically for testing
func (cf *connectionFactory) CreateTestConnection(conn net.Conn) (ConnectionManager, error) {
	// Use centralized testing configuration
	testConfig := NewConfigManagerForTesting()
	testPolicy := testConfig.ToTimeoutPolicy()

	return cf.CreateConnection(conn, testPolicy)
}

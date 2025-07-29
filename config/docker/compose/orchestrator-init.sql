-- Initialize orchestrator database for authentication

-- Set default privileges for postgres user
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO postgres;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO postgres;

-- Create users table for orchestrator authentication
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(255) PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL,
    username VARCHAR(150) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    first_name VARCHAR(150),
    last_name VARCHAR(150),
    role VARCHAR(50) DEFAULT 'user' NOT NULL,
    is_active BOOLEAN DEFAULT TRUE NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);

-- Create indexes for better performance
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_is_active ON users(is_active);

-- Insert default admin user (password: admin123)
-- Password hash is SHA256 of 'admin123' 
INSERT INTO users (id, email, username, password_hash, first_name, last_name, role) 
VALUES (
    'ad25b879-6f35-4cc0-bbd8-036afc3a0a07', 
    'admin@example.com', 
    'admin', 
    '240be518fabd2724ddb6f04eeb1da5967448d7e831c08c8fa822809f74c720a9', 
    'Admin', 
    'User', 
    'admin'
) ON CONFLICT (email) DO NOTHING;

-- Insert test users for development
INSERT INTO users (id, email, username, password_hash, first_name, last_name, role) 
VALUES 
    ('test-user-001', 'user@example.com', 'testuser', '240be518fabd2724ddb6f04eeb1da5967448d7e831c08c8fa822809f74c720a9', 'Test', 'User', 'user'),
    ('test-user-002', 'manager@example.com', 'testmanager', '240be518fabd2724ddb6f04eeb1da5967448d7e831c08c8fa822809f74c720a9', 'Test', 'Manager', 'manager')
ON CONFLICT (email) DO NOTHING;

-- Log successful initialization
SELECT 'Orchestrator database initialized successfully' AS status; 
-- Create database for API Service
-- This script runs when PostgreSQL container starts for the first time

-- Main database 'collabops' is created by POSTGRES_DB env var
-- Grant permissions for API service database
GRANT ALL PRIVILEGES ON DATABASE collabops TO postgres;

-- Print confirmation
\echo 'API Service database initialized successfully:'
\echo '  - collabops (API Service)' 
const express = require('express');
const app = express();
const port = 8082;

// Middleware to parse JSON requests
app.use(express.json());

// Log all incoming requests
app.use((req, res, next) => {
  console.log(`${new Date().toISOString()} - ${req.method} ${req.url}`);
  next();
});

// Mock permission endpoint - always return true for testing
app.get('/permissions/:userId/:owner/:repo', (req, res) => {
  const { userId, owner, repo } = req.params;
  const operation = req.query.op || 'read';
  
  console.log(`Permission check: user=${userId}, repo=${owner}/${repo}, op=${operation}`);
  
  // Always allow for testing file diff endpoint
  res.json({
    allowed: true,
    user: userId,
    repository: `${owner}/${repo}`,
    operation: operation,
    timestamp: new Date().toISOString()
  });
});

// Health check endpoint
app.get('/health', (req, res) => {
  res.json({ status: 'ok', service: 'mock-permission-service' });
});

// Start server
app.listen(port, () => {
  console.log(`Mock permission service running on http://localhost:${port}`);
  console.log('This service always allows all permissions for testing purposes');
}); 
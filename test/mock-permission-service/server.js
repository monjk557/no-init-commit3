const express = require('express');
const bodyParser = require('body-parser');

const app = express();
app.use(bodyParser.json());

// Mock permission data
const permissions = {
    'test-user-123': {
        'test-owner/test-repo': { read: true, write: true },
        'test-owner/read-only-repo': { read: true, write: false },
        'test-owner/no-access-repo': { read: false, write: false }
    },
    'readonly-user': {
        'test-owner/test-repo': { read: true, write: false }
    },
    'admin-user': {
        '*': { read: true, write: true } // Admin has access to all repos
    }
};

// Health check endpoint
app.get('/health', (req, res) => {
    res.json({ status: 'healthy' });
});

// Permission check endpoint
app.post('/v1/permissions/check', (req, res) => {
    const { user_id, owner, repo } = req.body;
    
    console.log(`Permission check: user=${user_id}, repo=${owner}/${repo}`);
    
    if (!user_id || !owner || !repo) {
        return res.status(400).json({ error: 'Missing required fields' });
    }
    
    // Check user permissions
    const userPerms = permissions[user_id];
    if (!userPerms) {
        return res.json({ read: false, write: false });
    }
    
    // Check for admin wildcard
    if (userPerms['*']) {
        return res.json(userPerms['*']);
    }
    
    // Check specific repo permissions
    const repoKey = `${owner}/${repo}`;
    const repoPerm = userPerms[repoKey];
    
    if (repoPerm) {
        return res.json(repoPerm);
    }
    
    // Default to no access
    res.json({ read: false, write: false });
});

// Mock user info endpoint
app.get('/v1/users/:userId', (req, res) => {
    const { userId } = req.params;
    
    const users = {
        'test-user-123': {
            id: 'test-user-123',
            username: 'testuser',
            email: 'test@example.com',
            name: 'Test User'
        },
        'readonly-user': {
            id: 'readonly-user',
            username: 'readonly',
            email: 'readonly@example.com',
            name: 'Read Only User'
        },
        'admin-user': {
            id: 'admin-user',
            username: 'admin',
            email: 'admin@example.com',
            name: 'Admin User',
            is_admin: true
        }
    };
    
    const user = users[userId];
    if (user) {
        res.json(user);
    } else {
        res.status(404).json({ error: 'User not found' });
    }
});

// Start server
const PORT = process.env.PORT || 8081;
app.listen(PORT, () => {
    console.log(`Mock permission service running on port ${PORT}`);
}); 
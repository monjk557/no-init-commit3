#!/usr/bin/env python3
import http.server
import socketserver
import json
import urllib.parse
from datetime import datetime

class MockPermissionHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        """Handle GET requests"""
        parsed_path = urllib.parse.urlparse(self.path)
        
        # Health check endpoint
        if parsed_path.path == '/health':
            self.send_response(200)
            self.send_header('Content-type', 'application/json')
            self.end_headers()
            response = {'status': 'ok', 'service': 'mock-permission-service'}
            self.wfile.write(json.dumps(response).encode())
            return
        
        # Permission check endpoint: /permissions/{userId}/{owner}/{repo}
        path_parts = parsed_path.path.strip('/').split('/')
        if len(path_parts) >= 4 and path_parts[0] == 'permissions':
            user_id = path_parts[1]
            owner = path_parts[2]
            repo = path_parts[3]
            
            # Parse query parameters
            query_params = urllib.parse.parse_qs(parsed_path.query)
            operation = query_params.get('op', ['read'])[0]
            
            print(f"Permission check: user={user_id}, repo={owner}/{repo}, op={operation}")
            
            # Always allow for testing
            self.send_response(200)
            self.send_header('Content-type', 'application/json')
            self.end_headers()
            
            response = {
                'allowed': True,
                'user': user_id,
                'repository': f"{owner}/{repo}",
                'operation': operation,
                'timestamp': datetime.now().isoformat()
            }
            
            self.wfile.write(json.dumps(response).encode())
            return
        
        # Default 404
        self.send_response(404)
        self.end_headers()
        self.wfile.write(b'404 Not Found')
    
    def log_message(self, format, *args):
        """Log requests with timestamps"""
        print(f"{datetime.now().isoformat()} - {format % args}")

if __name__ == '__main__':
    PORT = 8082
    
    with socketserver.TCPServer(("", PORT), MockPermissionHandler) as httpd:
        print(f"Mock permission service running on http://localhost:{PORT}")
        print("This service always allows all permissions for testing purposes")
        print("Endpoints:")
        print(f"  GET /health - Health check")
        print(f"  GET /permissions/{{userId}}/{{owner}}/{{repo}}?op={{operation}} - Permission check")
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            print("\nShutting down mock permission service...")
            httpd.shutdown() 
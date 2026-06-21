package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	DatabaseURL string
	Port        string
	BaseURL     string
}

type ShortenRequest struct {
	URL string `json:"url"`
}

type ShortenResponse struct {
	ShortURL string `json:"short_url"`
}

var db *sql.DB
var config Config

func main() {
	config = Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Port:        getEnv("PORT", "3000"), // Vercel injects the PORT dynamically
		BaseURL:     os.Getenv("BASE_URL"),  // Falls back to request host if empty
	}

	if config.DatabaseURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	var err error
	db, err = sql.Open("pgx", config.DatabaseURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	defer db.Close()

	// Restrict pool size so multiple dynamic environments do not exhaust connections
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("Database ping failed: %v", err)
	}

	// Schema creation
	createTableQuery := `
	CREATE TABLE IF NOT EXISTS urls (
		id SERIAL PRIMARY KEY,
		long_url TEXT NOT NULL,
		short_code VARCHAR(10) UNIQUE NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.Exec(createTableQuery); err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	mux := http.NewServeMux()

	// Go 1.22+ Standard routing
	mux.HandleFunc("POST /api/shorten", handleShorten)
	mux.HandleFunc("GET /swagger.json", handleSwaggerJSON)
	mux.HandleFunc("GET /docs", handleDocs)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	
	// Fallback/Wildcard redirect
	mux.HandleFunc("GET /{code}", handleRedirect)
    
	// Root endpoint redirects to API documentation
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		handleDocs(w, r)
	})

	log.Printf("Server starting on port %s...", config.Port)
	if err := http.ListenAndServe(":"+config.Port, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func generateCode(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		num, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[num.Int64()]
	}
	return string(b)
}

func handleShorten(w http.ResponseWriter, r *http.Request) {
	var req ShortenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		http.Error(w, `{"error": "Invalid request body or missing 'url'"}`, http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		http.Error(w, `{"error": "URL must start with http:// or https://"}`, http.StatusBadRequest)
		return
	}

	var shortCode string
	for i := 0; i < 5; i++ {
		shortCode = generateCode(6)
		_, err := db.ExecContext(r.Context(), "INSERT INTO urls (long_url, short_code) VALUES ($1, $2)", req.URL, shortCode)
		if err == nil {
			break
		}
		if i == 4 {
			http.Error(w, `{"error": "Failed to generate unique alias"}`, http.StatusInternalServerError)
			return
		}
	}

	currentBaseURL := config.BaseURL
	if currentBaseURL == "" {
		scheme := "https"
		if r.TLS == nil && !strings.Contains(r.Host, "localhost") {
			scheme = "https"
		} else if r.TLS == nil {
			scheme = "http"
		}
		currentBaseURL = fmt.Sprintf("%s://%s", scheme, r.Host)
	}

	resp := ShortenResponse{
		ShortURL: fmt.Sprintf("%s/%s", currentBaseURL, shortCode),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func handleRedirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if code == "" || len(code) > 10 {
		http.NotFound(w, r)
		return
	}

	var longURL string
	err := db.QueryRowContext(r.Context(), "SELECT long_url FROM urls WHERE short_code = $1", code).Scan(&longURL)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, longURL, http.StatusFound)
}

func handleSwaggerJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(swaggerJSON))
}

func handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(swaggerHTML))
}

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>URL Shortener API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: '/swagger.json',
        dom_id: '#swagger-ui',
      });
    };
  </script>
</body>
</html>`

const swaggerJSON = `{
  "openapi": "3.0.0",
  "info": {
    "title": "URL Shortener API",
    "version": "1.0.0",
    "description": "A simple URL shortener service built with Go and PostgreSQL on Vercel."
  },
  "paths": {
    "/api/shorten": {
      "post": {
        "summary": "Create a short URL",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "url": {
                    "type": "string",
                    "example": "https://www.google.com"
                  }
                },
                "required": ["url"]
              }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Shortened URL created successfully",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "short_url": {
                      "type": "string",
                      "example": "https://app-url.vercel.app/aB3dE9"
                    }
                  }
                }
              }
            }
          },
          "400": {
            "description": "Invalid input"
          }
        }
      }
    },
    "/{code}": {
      "get": {
        "summary": "Redirect to original URL",
        "parameters": [
          {
            "name": "code",
            "in": "path",
            "required": true,
            "schema": {
              "type": "string"
            }
          }
        ],
        "responses": {
          "302": {
            "description": "Redirect to the original long URL"
          },
          "404": {
            "description": "Short code not found"
          }
        }
      }
    }
  }
}`
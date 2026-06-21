package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type ShortenRequest struct {
	URL string `json:"url"`
}

type ShortenResponse struct {
	ShortURL string `json:"short_url"`
}

var (
	db      *sql.DB
	dbOnce  sync.Once
	dbErr   error
	baseURL string
)

func initDB() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbErr = fmt.Errorf("DATABASE_URL environment variable is required")
		return
	}

	db, dbErr = sql.Open("pgx", dbURL)
	if dbErr != nil {
		return
	}

	// Serverless Best Practice: Keep pools very small as multiple lambdas run concurrently
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(1 * time.Minute)

	if dbErr = db.Ping(); dbErr != nil {
		return
	}

	// Automatic database schema setup
	createTableQuery := `
	CREATE TABLE IF NOT EXISTS urls (
		id SERIAL PRIMARY KEY,
		long_url TEXT NOT NULL,
		short_code VARCHAR(10) UNIQUE NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, dbErr = db.Exec(createTableQuery)
}

// Handler is the entrypoint Vercel uses to process incoming requests
func Handler(w http.ResponseWriter, r *http.Request) {
	dbOnce.Do(initDB)
	if dbErr != nil {
		http.Error(w, "Database initialization failed: "+dbErr.Error(), http.StatusInternalServerError)
		return
	}

	// Dynamically deduce domain if BASE_URL environment variable is omitted
	baseURL = os.Getenv("BASE_URL")
	if baseURL == "" {
		scheme := "https"
		if r.TLS == nil && !strings.Contains(r.Host, "localhost") {
			scheme = "https"
		} else if r.TLS == nil {
			scheme = "http"
		}
		baseURL = fmt.Sprintf("%s://%s", scheme, r.Host)
	}

	query := r.URL.Query()
	action := query.Get("action")
	code := query.Get("code")

	switch {
	case action == "shorten" && r.Method == http.MethodPost:
		handleShorten(w, r)
	case action == "swagger_json" && r.Method == http.MethodGet:
		handleSwaggerJSON(w, r)
	case action == "docs" && r.Method == http.MethodGet:
		handleDocs(w, r)
	case code != "":
		handleRedirect(w, r, code)
	default:
		handleDocs(w, r)
	}
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

	resp := ShortenResponse{
		ShortURL: fmt.Sprintf("%s/%s", baseURL, shortCode),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func handleRedirect(w http.ResponseWriter, r *http.Request, code string) {
	if len(code) > 10 {
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
package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v4/neo4j"
)

// Config holds application configuration
type Config struct {
	Neo4jURI      string
	Neo4jUser     string
	Neo4jPassword string
	ModelPath     string
	EmbeddingURL  string
	LLMServerURL  string
	MaxChunkSize  int
	ChunkOverlap  int
	CodeDir       string
	DbName        string
}

// CodeChunk represents a chunk of code with metadata
type CodeChunk struct {
	ID          string   `json:"id"`
	Content     string   `json:"content"`
	FilePath    string   `json:"file_path"`
	ProjectPath string   `json:"project_path"`
	Language    string   `json:"language"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	EntityType  string   `json:"entity_type"` // "function", "class", "method", "chunk"
	Name        string   `json:"name"`        // function/class name if available
	Signature   string   `json:"signature"`   // function signature if available
	Embedding   []float32 `json:"-"`         // Vector embedding (not stored in JSON)
	Hash        string   `json:"hash"`        // Content hash for change detection
	Score       float64  `json:"score"`       // Similarity score from search
}

// LLMRequest represents a request to the LLM
type LLMRequest struct {
	Prompt    string  `json:"prompt"`
	MaxTokens int     `json:"max_tokens"`
	Temperature float32 `json:"temperature"`
}

// LLMResponse represents a response from the LLM
type LLMResponse struct {
	Text     string `json:"text"`
	TokensUsed int  `json:"tokens_used"`
}

// EmbeddingRequest represents a request to the embedding service
type EmbeddingRequest struct {
	Texts []string `json:"texts"`
}

// EmbeddingResponse represents a response from the embedding service
type EmbeddingResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Neo4jRAG handles storing and retrieving code chunks from Neo4j
type Neo4jRAG struct {
	driver neo4j.Driver
	config Config
	logger *log.Logger
}

// NewNeo4jRAG creates a new Neo4jRAG instance
func NewNeo4jRAG(config Config) (*Neo4jRAG, error) {
	logger := log.New(os.Stdout, "NEO4J-RAG: ", log.LstdFlags)
	
	// Connect to Neo4j
	logger.Println("Connecting to Neo4j at", config.Neo4jURI)
	driver, err := neo4j.NewDriver(config.Neo4jURI, neo4j.BasicAuth(config.Neo4jUser, config.Neo4jPassword, ""))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Neo4j: %w", err)
	}
	
	// Test the connection
	err = driver.VerifyConnectivity()
	if err != nil {
		return nil, fmt.Errorf("failed to verify Neo4j connectivity: %w", err)
	}
	
	logger.Println("Successfully connected to Neo4j")
	
	rag := &Neo4jRAG{
		driver: driver,
		config: config,
		logger: logger,
	}
	
	// Initialize database
	err = rag.initDatabase()
	if err != nil {
		driver.Close()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	
	return rag, nil
}

// Close closes the Neo4j connection
func (r *Neo4jRAG) Close() {
	r.driver.Close()
}

// initDatabase sets up the Neo4j database schema
func (r *Neo4jRAG) initDatabase() error {
	session := r.driver.NewSession(neo4j.SessionConfig{})
	defer session.Close()
	
	// Create constraints and indexes
	constraints := []string{
		"CREATE CONSTRAINT chunk_id IF NOT EXISTS ON (c:Chunk) ASSERT c.id IS UNIQUE",
		"CREATE CONSTRAINT file_path IF NOT EXISTS ON (f:File) ASSERT f.path IS UNIQUE",
		"CREATE CONSTRAINT project_path IF NOT EXISTS ON (p:Project) ASSERT p.path IS UNIQUE",
		"CREATE INDEX chunk_hash IF NOT EXISTS FOR (c:Chunk) ON (c.hash)",
		"CREATE INDEX chunk_language IF NOT EXISTS FOR (c:Chunk) ON (c.language)",
		"CREATE INDEX chunk_entity_type IF NOT EXISTS FOR (c:Chunk) ON (c.entity_type)",
	}
	
	for _, constraint := range constraints {
		_, err := session.Run(constraint, nil)
		if err != nil {
			return fmt.Errorf("failed to create constraint: %w", err)
		}
	}
	
	// Check if GDS library is available
	gdsResult, gdsErr := session.Run("CALL gds.list() YIELD name RETURN count(name) as count", nil)
	if gdsErr != nil {
		r.logger.Printf("Warning: Graph Data Science library might not be installed: %v\n", gdsErr)
	} else {
		if gdsResult.Next() {
			count, _ := gdsResult.Record().Get("count")
			r.logger.Printf("GDS library initialized with %v procedures\n", count)
		}
	}
	
	return nil
}

// IndexDirectory indexes a directory of code
func (r *Neo4jRAG) IndexDirectory(dir string) error {
	r.logger.Printf("Indexing directory: %s\n", dir)
	
	// Get all code files recursively
	files, err := r.findCodeFiles(dir)
	if err != nil {
		return fmt.Errorf("failed to find code files: %w", err)
	}
	
	r.logger.Printf("Found %d files to index\n", len(files))
	
	// Process each file
	for i, file := range files {
		if i%100 == 0 && i > 0 {
			r.logger.Printf("Processed %d/%d files\n", i, len(files))
		}
		
		err := r.processFile(file, dir)
		if err != nil {
			r.logger.Printf("Error processing file %s: %v\n", file, err)
		}
	}
	
	r.logger.Printf("Indexing complete. Processed %d files\n", len(files))
	return nil
}

// findCodeFiles recursively finds all code files in a directory with comprehensive filtering
func (r *Neo4jRAG) findCodeFiles(root string) ([]string, error) {
	var files []string
	
	// Extensions to include - expanded list of code file extensions
	extensions := map[string]bool{
		// Programming languages
		".go":    true,
		".py":    true,
		".js":    true,
		".jsx":   true,
		".ts":    true,
		".tsx":   true,
		".java":  true,
		".c":     true,
		".cpp":   true,
		".cc":    true,
		".cxx":   true,
		".h":     true,
		".hpp":   true,
		".hxx":   true,
		".cs":    true,
		".php":   true,
		".rb":    true,
		".rs":    true,
		".swift": true,
		".kt":    true,
		".scala": true,
		".pl":    true,
		".pm":    true,
		".r":     true,
		".lua":   true,
		".groovy":true,
		".dart":  true,
		".elm":   true,
		".ex":    true,
		".exs":   true,
		".erl":   true,
		".hrl":   true,
		".clj":   true,
		".hs":    true,
		".fs":    true,
		".fsx":   true,
		".ml":    true,
		".mli":   true,
		
		// Shell scripts
		".sh":    true,
		".bash":  true,
		".zsh":   true,
		".fish":  true,
		".ps1":   true,
		".bat":   true,
		".cmd":   true,
		
		// Web development
		".html":  true,
		".htm":   true,
		".xhtml": true,
		".css":   true,
		".scss":  true,
		".sass":  true,
		".less":  true,
		".vue":   true,
		".svelte":true,
		
		// Data and config files
		".json":  true,
		".yaml":  true,
		".yml":   true,
		".xml":   true,
		".toml":  true,
		".ini":   true,
		".sql":   true,
		".graphql":true,
		".proto": true,
		
		// Documentation
		".md":    true,
		".rst":   true,
		".tex":   true,
		".adoc":  true,
	}
	
	// Directories to ignore - expanded with more common patterns
	ignoreDirs := map[string]bool{
		// Package managers and dependencies
		"node_modules":    true,
		"vendor":          true,
		"bower_components":true,
		"jspm_packages":   true,
		"packages":        true,
		
		// Version control
		".git":            true,
		".svn":            true,
		".hg":             true,
		".bzr":            true,
		
		// Virtual environments
		".venv":           true,
		"venv":            true,
		"env":             true,
		".env":            true,
		"virtualenv":      true,
		"__pycache__":     true,
		"site-packages":   true,
		
		// Build and distribution
		"dist":            true,
		"build":           true,
		"out":             true,
		"bin":             true,
		"target":          true,
		"output":          true,
		"release":         true,
		"debug":           true,
		
		// IDE and editor
		".idea":           true,
		".vscode":         true,
		".vs":             true,
		".eclipse":        true,
		".settings":       true,
		
		// Temporary and cache
		"tmp":             true,
		"temp":            true,
		"cache":           true,
		".cache":          true,
		".sass-cache":     true,
		
		// Documentation
		"docs":            true,
		"doc":             true,
		
		// Test coverage
		"coverage":        true,
		".nyc_output":     true,
		".coverage":       true,
		"htmlcov":         true,
		
		// Logs
		"logs":            true,
		"log":             true,
	}
	
	// Files to ignore (by pattern)
	ignoreFilePatterns := []string{
		// Minified files
		"*.min.js",
		"*.min.css",
		
		// Generated files
		"*.generated.*",
		"*_generated.*",
		"*.g.*",
		"*.pb.*",
		
		// Compiled binaries
		"*.exe",
		"*.dll",
		"*.so",
		"*.dylib",
		"*.class",
		"*.o",
		"*.obj",
		"*.a",
		"*.lib",
		"*.pyc",
		"*.pyo",
		
		// Archives
		"*.zip",
		"*.tar",
		"*.gz",
		"*.bz2",
		"*.xz",
		"*.rar",
		"*.7z",
		
		// Media files
		"*.jpg", "*.jpeg",
		"*.png",
		"*.gif",
		"*.bmp",
		"*.ico",
		"*.svg",
		"*.webp",
		"*.mp3",
		"*.mp4",
		"*.wav",
		"*.avi",
		"*.mov",
		"*.webm",
		
		// Lock files
		"*.lock",
		"package-lock.json",
		"yarn.lock",
		"Cargo.lock",
		
		// Backup files
		"*~",
		"*.bak",
		"*.swp",
		"*.swo",
		
		// Large data files
		"*.csv",
		"*.tsv",
		"*.db",
		"*.sqlite",
		"*.sqlite3",
		
		// Logs
		"*.log",
	}
	
	// Maximum file size to process (1MB)
	maxFileSize := int64(1 * 1024 * 1024)
	
	r.logger.Printf("Starting file indexing with enhanced filtering from root: %s\n", root)
	
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			r.logger.Printf("Error accessing path %s: %v\n", path, err)
			return nil // Continue walking despite the error
		}
		
		// Skip if file is too large
		if !info.IsDir() && info.Size() > maxFileSize {
			r.logger.Printf("Skipping large file: %s (%.2f MB)\n", path, float64(info.Size())/(1024*1024))
			return nil
		}
		
		// Handle directories
		if info.IsDir() {
			// Check if we should skip this directory
			baseName := filepath.Base(path)
			
			// Skip hidden directories (starting with .)
			if strings.HasPrefix(baseName, ".") && baseName != "." && baseName != ".." {
				return filepath.SkipDir
			}
			
			// Check for direct matches with excluded directories
			if ignoreDirs[baseName] {
				r.logger.Printf("Skipping directory: %s\n", path)
				return filepath.SkipDir
			}
			
			// Check for path components that should be skipped
			pathParts := strings.Split(path, string(os.PathSeparator))
			for _, part := range pathParts {
				if ignoreDirs[part] {
					r.logger.Printf("Skipping directory path containing %s: %s\n", part, path)
					return filepath.SkipDir
				}
			}
			
			// Check for virtual environment paths
			if (strings.Contains(path, "venv/lib/python") && strings.Contains(path, "site-packages")) ||
			   (strings.Contains(path, "env/lib/python") && strings.Contains(path, "site-packages")) {
				r.logger.Printf("Skipping Python virtual environment path: %s\n", path)
				return filepath.SkipDir
			}
			
			return nil
		}
		
		// Handle files
		fileName := filepath.Base(path)
		
		// Skip hidden files
		if strings.HasPrefix(fileName, ".") {
			return nil
		}
		
		// Skip files matching ignore patterns
		for _, pattern := range ignoreFilePatterns {
			matched, err := filepath.Match(pattern, fileName)
			if err != nil {
				r.logger.Printf("Error matching pattern %s: %v\n", pattern, err)
				continue
			}
			if matched {
				return nil
			}
		}
		
		// Check if file extension is one we want to process
		ext := strings.ToLower(filepath.Ext(path))
		if extensions[ext] {
			r.logger.Printf("Including file: %s\n", path)
			files = append(files, path)
		}
		
		return nil
	})
	
	r.logger.Printf("File filtering complete. Found %d files to process\n", len(files))
	return files, err
}

// processFile processes a single code file
func (r *Neo4jRAG) processFile(filePath, rootDir string) error {
	// Read file
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}
	
	// Skip if file is too large (>1MB)
	if len(content) > 1024*1024 {
		r.logger.Printf("Skipping large file: %s (%d bytes)\n", filePath, len(content))
		return nil
	}
	
	// Get file info
	relPath, err := filepath.Rel(rootDir, filePath)
	if err != nil {
		relPath = filePath
	}
	
	ext := strings.ToLower(filepath.Ext(filePath))
	language := getLanguageFromExt(ext)
	
	// Determine project path (typically the first directory in the relative path)
	projectPath := rootDir
	pathParts := strings.Split(relPath, string(filepath.Separator))
	if len(pathParts) > 1 {
		projectPath = filepath.Join(rootDir, pathParts[0])
	}
	
	// Chunk the file
	chunks, err := r.chunkFile(string(content), filePath, projectPath, language)
	if err != nil {
		return fmt.Errorf("failed to chunk file: %w", err)
	}
	
	// Skip if no chunks were created
	if len(chunks) == 0 {
		return nil
	}
	
	// Generate embeddings for chunks
	err = r.generateEmbeddings(chunks)
	if err != nil {
		return fmt.Errorf("failed to generate embeddings: %w", err)
	}
	
	// Store chunks in Neo4j
	err = r.storeChunks(chunks, filePath, projectPath)
	if err != nil {
		return fmt.Errorf("failed to store chunks: %w", err)
	}
	
	return nil
}

// chunkFile splits a file into chunks
func (r *Neo4jRAG) chunkFile(content, filePath, projectPath, language string) ([]CodeChunk, error) {
	var chunks []CodeChunk
	
	// For Go files, try to split by functions/methods
	if language == "Go" {
		chunks = r.chunkGoCode(content, filePath, projectPath)
	}
	
	// For other languages or if function chunking produced too few chunks
	if len(chunks) < 2 {
		chunks = r.chunkBySize(content, filePath, projectPath, language)
	}
	
	// Generate IDs and hashes for chunks
	for i := range chunks {
		// Generate a deterministic ID based on file path and chunk position
		idStr := fmt.Sprintf("%s:%d:%d", filePath, chunks[i].StartLine, chunks[i].EndLine)
		h := md5.Sum([]byte(idStr))
		chunks[i].ID = hex.EncodeToString(h[:])
		
		// Generate content hash for change detection
		contentHash := md5.Sum([]byte(chunks[i].Content))
		chunks[i].Hash = hex.EncodeToString(contentHash[:])
	}
	
	return chunks, nil
}

// chunkGoCode splits Go code by functions and methods
func (r *Neo4jRAG) chunkGoCode(content, filePath, projectPath string) []CodeChunk {
	chunks := []CodeChunk{}
	
	// Regex patterns for Go functions
	funcPattern := regexp.MustCompile(`func\s+(\w+)\s*\((.*?)\)(?:\s+\w+)?\s*{`)
	methodPattern := regexp.MustCompile(`func\s+\(\w+\s+\*?\w+\)\s+(\w+)\s*\((.*?)\)(?:\s+\w+)?\s*{`)
	
	// Find all functions
	funcMatches := funcPattern.FindAllStringSubmatchIndex(content, -1)
	methodMatches := methodPattern.FindAllStringSubmatchIndex(content, -1)
	
	// Combine and sort all matches by their start position
	type match struct {
		start    int
		end      int
		name     string
		sig      string
		isMethod bool
	}
	
	allMatches := []match{}
	
	// Process function matches
	for _, m := range funcMatches {
		if len(m) >= 4 {
			funcName := content[m[2]:m[3]]
			signature := ""
			if len(m) >= 6 {
				signature = content[m[4]:m[5]]
			}
			allMatches = append(allMatches, match{
				start:    m[0],
				end:      m[1],
				name:     funcName,
				sig:      signature,
				isMethod: false,
			})
		}
	}
	
	// Process method matches
	for _, m := range methodMatches {
		if len(m) >= 4 {
			methodName := content[m[2]:m[3]]
			signature := ""
			if len(m) >= 6 {
				signature = content[m[4]:m[5]]
			}
			allMatches = append(allMatches, match{
				start:    m[0],
				end:      m[1],
				name:     methodName,
				sig:      signature,
				isMethod: true,
			})
		}
	}
	
	// Sort by start position
	sort.Slice(allMatches, func(i, j int) bool {
		return allMatches[i].start < allMatches[j].start
	})
	
	// Create chunks from matches
	lines := strings.Split(content, "\n")
	linePositions := make([]int, len(lines)+1)
	pos := 0
	for i, line := range lines {
		linePositions[i] = pos
		pos += len(line) + 1 // +1 for newline
	}
	linePositions[len(lines)] = pos
	
	for i, m := range allMatches {
		startPos := m.start
		var endPos int
		
		// End position is either the start of next function or end of file
		if i < len(allMatches)-1 {
			endPos = allMatches[i+1].start
		} else {
			endPos = len(content)
		}
		
		// Find start and end lines
		startLine := sort.Search(len(linePositions), func(i int) bool {
			return linePositions[i] > startPos
		}) - 1
		if startLine < 0 {
			startLine = 0
		}
		
		endLine := sort.Search(len(linePositions), func(i int) bool {
			return linePositions[i] > endPos
		}) - 1
		if endLine < 0 {
			endLine = 0
		}
		
		// Create chunk
		entityType := "function"
		if m.isMethod {
			entityType = "method"
		}
		
		chunks = append(chunks, CodeChunk{
			FilePath:    filePath,
			ProjectPath: projectPath,
			Content:     content[startPos:endPos],
			StartLine:   startLine + 1, // 1-based line numbers
			EndLine:     endLine + 1,
			EntityType:  entityType,
			Name:        m.name,
			Signature:   m.sig,
			Language:    "Go",
		})
	}
	
	return chunks
}

// chunkBySize splits content into chunks of approximately equal size
func (r *Neo4jRAG) chunkBySize(content, filePath, projectPath, language string) []CodeChunk {
	chunks := []CodeChunk{}
	lines := strings.Split(content, "\n")
	
	// If file is small enough, return as single chunk
	if len(content) <= r.config.MaxChunkSize {
		return []CodeChunk{
			{
				FilePath:    filePath,
				ProjectPath: projectPath,
				Content:     content,
				StartLine:   1,
				EndLine:     len(lines),
				EntityType:  "chunk",
				Name:        fmt.Sprintf("chunk_1_%d", len(lines)),
				Language:    language,
			},
		}
	}
	
	// Otherwise, split into multiple chunks
	currentChunk := []string{}
	currentSize := 0
	startLine := 1
	
	for i, line := range lines {
		lineLen := len(line) + 1 // +1 for newline
		currentChunk = append(currentChunk, line)
		currentSize += lineLen
		
		// If chunk is big enough or we're at the end, save it
		if currentSize >= r.config.MaxChunkSize || i == len(lines)-1 {
			chunkContent := strings.Join(currentChunk, "\n")
			endLine := startLine + len(currentChunk) - 1
			
			chunks = append(chunks, CodeChunk{
				FilePath:    filePath,
				ProjectPath: projectPath,
				Content:     chunkContent,
				StartLine:   startLine,
				EndLine:     endLine,
				EntityType:  "chunk",
				Name:        fmt.Sprintf("chunk_%d_%d", startLine, endLine),
				Language:    language,
			})
			
			// Start new chunk with overlap
			overlapLines := r.config.ChunkOverlap
			if overlapLines > len(currentChunk) {
				overlapLines = len(currentChunk)
			}
			
			currentChunk = currentChunk[len(currentChunk)-overlapLines:]
			startLine = endLine - overlapLines + 1
			currentSize = 0
			for _, line := range currentChunk {
				currentSize += len(line) + 1
			}
		}
	}
	
	return chunks
}

// generateEmbeddings generates embeddings for chunks
func (r *Neo4jRAG) generateEmbeddings(chunks []CodeChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	
	// Prepare texts for embedding
	texts := make([]string, len(chunks))
	for i, chunk := range chunks {
		texts[i] = chunk.Content
	}
	
	// Call embedding service
	embeddings, err := r.getEmbeddings(texts)
	if err != nil {
		return fmt.Errorf("failed to generate embeddings: %w", err)
	}
	
	// Assign embeddings to chunks
	for i, embedding := range embeddings {
		chunks[i].Embedding = embedding
	}
	
	return nil
}

// getEmbeddings calls the embedding service
func (r *Neo4jRAG) getEmbeddings(texts []string) ([][]float32, error) {
	// Prepare request
	req := EmbeddingRequest{
		Texts: texts,
	}
	
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	
	// Call embedding service
	resp, err := http.Post(r.config.EmbeddingURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	// Parse response
	var embeddingResp EmbeddingResponse
	err = json.NewDecoder(resp.Body).Decode(&embeddingResp)
	if err != nil {
		return nil, err
	}
	
	return embeddingResp.Embeddings, nil
}

// storeChunks stores chunks in Neo4j
func (r *Neo4jRAG) storeChunks(chunks []CodeChunk, filePath, projectPath string) error {
	session := r.driver.NewSession(neo4j.SessionConfig{})
	defer session.Close()
	
	// Create a transaction
	_, err := session.WriteTransaction(func(tx neo4j.Transaction) (interface{}, error) {
		// Create/merge project node
		_, err := tx.Run(
			`MERGE (p:Project {path: $projectPath}) 
			 ON CREATE SET p.created_at = datetime(),
			               p.name = $projectName
			 ON MATCH SET p.updated_at = datetime()`,
			map[string]interface{}{
				"projectPath": projectPath,
				"projectName": filepath.Base(projectPath),
			},
		)
		if err != nil {
			return nil, err
		}
		
		// Create/merge file node
		_, err = tx.Run(
			`MERGE (f:File {path: $filePath}) 
			 ON CREATE SET f.created_at = datetime(),
			               f.name = $fileName,
			               f.language = $language
			 ON MATCH SET f.updated_at = datetime()
			 WITH f
			 MATCH (p:Project {path: $projectPath})
			 MERGE (f)-[:BELONGS_TO]->(p)`,
			map[string]interface{}{
				"filePath":    filePath,
				"fileName":    filepath.Base(filePath),
				"language":    getLanguageFromExt(filepath.Ext(filePath)),
				"projectPath": projectPath,
			},
		)
		if err != nil {
			return nil, err
		}
		
		// Store each chunk
		for _, chunk := range chunks {
			// Check if chunk exists with same hash (unchanged)
			result, err := tx.Run(
				"MATCH (c:Chunk {id: $id}) RETURN c.hash",
				map[string]interface{}{"id": chunk.ID},
			)
			if err != nil {
				return nil, err
			}
			
			record, err := result.Single()
			if err == nil { // Chunk exists
				storedHash, _ := record.Get("c.hash")
				if storedHash.(string) == chunk.Hash {
					// Skip if hash is the same (content unchanged)
					continue
				}
			}
			
			// Create/update chunk node with embedding
			params := map[string]interface{}{
				"id":          chunk.ID,
				"content":     chunk.Content,
				"filePath":    chunk.FilePath,
				"startLine":   chunk.StartLine,
				"endLine":     chunk.EndLine,
				"entityType":  chunk.EntityType,
				"name":        chunk.Name,
				"signature":   chunk.Signature,
				"language":    chunk.Language,
				"hash":        chunk.Hash,
				"embedding":   chunk.Embedding,
				"projectPath": chunk.ProjectPath,
				"updated_at":  time.Now().Format(time.RFC3339),
			}
			
			_, err = tx.Run(
				`MERGE (c:Chunk {id: $id})
				 ON CREATE SET c.created_at = datetime()
				 SET c.content = $content,
				     c.file_path = $filePath,
				     c.start_line = $startLine,
				     c.end_line = $endLine,
				     c.entity_type = $entityType,
				     c.name = $name,
				     c.signature = $signature,
				     c.language = $language,
				     c.hash = $hash,
				     c.embedding = $embedding,
				     c.updated_at = $updated_at
				 WITH c
				 MATCH (f:File {path: $filePath})
				 MERGE (c)-[:PART_OF]->(f)`,
				params,
			)
			if err != nil {
				return nil, err
			}
		}
		
		return nil, nil
	})
	
	return err
}

// SearchCode searches for code using vector similarity
func (r *Neo4jRAG) SearchCode(query string, limit int) ([]CodeChunk, error) {
	// Generate embedding for query
	fmt.Println("Generating embedding for query...")
	embeddings, err := r.getEmbeddings([]string{query})
	if err != nil {
		fmt.Printf("Error generating embedding: %v\n", err)
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}
	
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		fmt.Println("Received empty embedding for query")
		return nil, fmt.Errorf("received empty embedding for query")
	}
	
	fmt.Printf("Embedding generated successfully, length: %d\n", len(embeddings[0]))
	queryEmbedding := embeddings[0]
	
	// Search Neo4j
	fmt.Println("Searching Neo4j with similarity threshold > 0.1...")
	session := r.driver.NewSession(neo4j.SessionConfig{})
	defer session.Close()
	
	result, err := session.ReadTransaction(func(tx neo4j.Transaction) (interface{}, error) {
		// First check if the database has chunks
			fmt.Println("Checking database content...")
			testResult, testErr := tx.Run(
				`MATCH (c:Chunk) RETURN count(c) as count`,
				map[string]interface{}{},
			)
			
			if testErr != nil {
				fmt.Printf("Database check failed: %v\n", testErr)
				return nil, testErr
			}
			
			var chunkCount int64 = 0
			if testResult.Next() {
				count, _ := testResult.Record().Get("count")
				chunkCount = count.(int64)
				fmt.Printf("Database contains %v chunks\n", chunkCount)
				
				// If count is 0, no data was indexed
				if chunkCount == 0 {
					fmt.Println("No chunks found in database. Please run indexing first.")
					return []CodeChunk{}, nil
				}
			} else {
				fmt.Println("Could not get chunk count from database")
			}
			
			// Check if GDS library is installed and the vector index exists
			fmt.Println("Checking GDS library status...")
			gdsResult, gdsErr := tx.Run(
				`CALL gds.list() YIELD name RETURN count(name) as count`,
				map[string]interface{}{},
			)
			
			if gdsErr != nil {
				fmt.Printf("GDS library check failed: %v\n", gdsErr)
				fmt.Println("The Graph Data Science library might not be installed or configured properly.")
			} else if gdsResult.Next() {
				gdsCount, _ := gdsResult.Record().Get("count")
				fmt.Printf("GDS library has %v procedures available\n", gdsCount)
			}
			
			// Now try the vector similarity search with a very low threshold
			fmt.Println("Performing vector similarity search with threshold 0.1...")
			result, err := tx.Run(
				`MATCH (c:Chunk)
				 WITH c, gds.similarity.cosine(c.embedding, $embedding) AS vectorScore
				 
				 // Apply basic similarity threshold
				 WHERE vectorScore > 0.1
				 
				 // Calculate additional relevance factors
				 WITH c, vectorScore,
					  // Boost score for function/method chunks (more focused)
					  CASE WHEN c.entity_type IN ['function', 'method'] THEN 0.1 ELSE 0 END AS entityBoost,
					  
					  // Boost score for shorter chunks (more precise)
					  CASE WHEN size(c.content) < 500 THEN 0.05 ELSE 0 END AS sizeBoost,
					  
					  // Penalize very large chunks (too general)
					  CASE WHEN size(c.content) > 2000 THEN -0.05 ELSE 0 END AS sizePenalty
				 
				 // Calculate final score with boosts
				 WITH c, (vectorScore + entityBoost + sizeBoost + sizePenalty) AS score
				 
				 // Ensure minimum threshold even after adjustments
				 WHERE score > 0.1
				 
				 // Return results
				 RETURN c.id, c.content, c.file_path, c.start_line, c.end_line, 
						c.entity_type, c.name, c.signature, c.language, score
				 
				 // Order by final score and limit results
				 ORDER BY score DESC
				 LIMIT $limit`,
				map[string]interface{}{
					"embedding": queryEmbedding,
					"limit":     limit,
				},
			)
		
		if err != nil {
			return nil, err
		}
		
		chunks := []CodeChunk{}
		for result.Next() {
			record := result.Record()
			
			id, _ := record.Get("c.id")
			content, _ := record.Get("c.content")
			filePath, _ := record.Get("c.file_path")
			startLine, _ := record.Get("c.start_line")
			endLine, _ := record.Get("c.end_line")
			entityType, _ := record.Get("c.entity_type")
			name, _ := record.Get("c.name")
			signature, _ := record.Get("c.signature")
			language, _ := record.Get("c.language")
			score, _ := record.Get("score")
			
			chunk := CodeChunk{
				ID:         id.(string),
				Content:    content.(string),
				FilePath:   filePath.(string),
				StartLine:  int(startLine.(int64)),
				EndLine:    int(endLine.(int64)),
				EntityType: entityType.(string),
				Name:       name.(string),
				Language:   language.(string),
			}
			
			if signature != nil {
				chunk.Signature = signature.(string)
			}
			
			// Save the score in the chunk
			chunk.Score = score.(float64)
			
			r.logger.Printf("Found chunk with score %f: %s\n", score.(float64), chunk.Name)
			chunks = append(chunks, chunk)
		}
		
		return chunks, nil
	})
	
	if err != nil {
		fmt.Printf("Neo4j search failed: %v\n", err)
		return nil, fmt.Errorf("search failed: %w", err)
	}
	
	chunks := result.([]CodeChunk)
	fmt.Printf("Search complete. Found %d matching chunks\n", len(chunks))
	return chunks, nil
}

// SearchCodeAdvanced searches for code with advanced filtering options
func (r *Neo4jRAG) SearchCodeAdvanced(query string, limit int, languages []string, pathFilters []string, minScore float64, useKeywords bool) ([]CodeChunk, error) {
	// Generate embedding for query
	fmt.Println("Generating embedding for query...")
	embeddings, err := r.getEmbeddings([]string{query})
	if err != nil {
		fmt.Printf("Error generating embedding: %v\n", err)
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}
	
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		fmt.Println("Received empty embedding for query")
		return nil, fmt.Errorf("received empty embedding for query")
	}
	
	fmt.Printf("Embedding generated successfully, length: %d\n", len(embeddings[0]))
	queryEmbedding := embeddings[0]
	
	// Extract keywords for potential keyword search
	keywords := extractKeywords(query)
	
	// Search Neo4j
	fmt.Printf("Searching Neo4j with similarity threshold > %.2f...\n", minScore)
	session := r.driver.NewSession(neo4j.SessionConfig{})
	defer session.Close()
	
	result, err := session.ReadTransaction(func(tx neo4j.Transaction) (interface{}, error) {
		// First check if the database has chunks
		fmt.Println("Checking database content...")
		testResult, testErr := tx.Run(
			`MATCH (c:Chunk) RETURN count(c) as count`,
			map[string]interface{}{},
		)
		
		if testErr != nil {
			fmt.Printf("Database check failed: %v\n", testErr)
			return nil, testErr
		}
		
		var chunkCount int64 = 0
		if testResult.Next() {
			count, _ := testResult.Record().Get("count")
			chunkCount = count.(int64)
			fmt.Printf("Database contains %v chunks\n", chunkCount)
			
			// If count is 0, no data was indexed
			if chunkCount == 0 {
				fmt.Println("No chunks found in database. Please run indexing first.")
				return []CodeChunk{}, nil
			}
		} else {
			fmt.Println("Could not get chunk count from database")
		}
		
		// Build the Cypher query with filters
		cypherQuery := `MATCH (c:Chunk)`
		
		// Add language filter if specified
		if len(languages) > 0 {
			cypherQuery += ` WHERE c.language IN $languages`
		}
		
		// Add path filter if specified
		if len(pathFilters) > 0 {
			if len(languages) > 0 {
				cypherQuery += ` AND`
			} else {
				cypherQuery += ` WHERE`
			}
			
			pathConditions := []string{}
			for i := range pathFilters {
				// Use pattern index for parameter name
				pathConditions = append(pathConditions, fmt.Sprintf(`c.file_path =~ $pathPattern%d`, i))
			}
			cypherQuery += ` (` + strings.Join(pathConditions, ` OR `) + `)`
		}
		
		// Add keyword search if enabled
		if useKeywords && len(keywords) > 0 {
			keywordCondition := ``
			if strings.Contains(cypherQuery, `WHERE`) {
				keywordCondition += ` AND (`
			} else {
				keywordCondition += ` WHERE (`
			}
			
			keywordPatterns := []string{}
			for i, keyword := range keywords {
				if len(keyword) > 3 { // Only use keywords with more than 3 characters
					keywordPatterns = append(keywordPatterns, 
						fmt.Sprintf(`c.content CONTAINS $keyword%d`, i))
				}
			}
			
			if len(keywordPatterns) > 0 {
				keywordCondition += strings.Join(keywordPatterns, ` OR `) + `)`
				cypherQuery += keywordCondition
			}
		}
		
		// Add vector similarity calculation and improved scoring
		cypherQuery += `
		WITH c, gds.similarity.cosine(c.embedding, $embedding) AS vectorScore
		
		// Apply basic similarity threshold
		WHERE vectorScore > $minScore
		
		// Calculate additional relevance factors
		WITH c, vectorScore,
		     // Boost score for function/method chunks (more focused)
		     CASE WHEN c.entity_type IN ['function', 'method'] THEN 0.1 ELSE 0 END AS entityBoost,
		     
		     // Boost score for shorter chunks (more precise)
		     CASE WHEN size(c.content) < 500 THEN 0.05 ELSE 0 END AS sizeBoost,
		     
		     // Penalize very large chunks (too general)
		     CASE WHEN size(c.content) > 2000 THEN -0.05 ELSE 0 END AS sizePenalty

		// Calculate final score with boosts
		WITH c, (vectorScore + entityBoost + sizeBoost + sizePenalty) AS score
		
		// Ensure minimum threshold even after adjustments
		WHERE score > $minScore
		
		// Return results
		RETURN c.id, c.content, c.file_path, c.project_path, c.start_line, c.end_line, 
		       c.entity_type, c.name, c.signature, c.language, score
		
		// Order by final score and limit results
		ORDER BY score DESC
		LIMIT $limit`
		
		// Prepare parameters
		parameters := map[string]interface{}{
			"embedding": queryEmbedding,
			"minScore":  minScore,
			"limit":     limit,
		}
		
		// Add language parameters if specified
		if len(languages) > 0 {
			parameters["languages"] = languages
		}
		
		// Add path filter parameters if specified
		for i, pattern := range pathFilters {
			parameters[fmt.Sprintf("pathPattern%d", i)] = globToRegex(pattern)
		}
		
		// Add keyword parameters if enabled
		if useKeywords && len(keywords) > 0 {
			for i, keyword := range keywords {
				if len(keyword) > 3 {
					parameters[fmt.Sprintf("keyword%d", i)] = keyword
				}
			}
		}
		
		// Execute the query
		result, err := tx.Run(cypherQuery, parameters)
		
		if err != nil {
			return nil, err
		}
		
		chunks := []CodeChunk{}
		for result.Next() {
			record := result.Record()
			
			id, _ := record.Get("c.id")
			content, _ := record.Get("c.content")
			filePath, _ := record.Get("c.file_path")
			startLine, _ := record.Get("c.start_line")
			endLine, _ := record.Get("c.end_line")
			entityType, _ := record.Get("c.entity_type")
			name, _ := record.Get("c.name")
			signature, _ := record.Get("c.signature")
			language, _ := record.Get("c.language")
			score, _ := record.Get("score")
			
			chunk := CodeChunk{
				ID:         id.(string),
				Content:    content.(string),
				FilePath:   filePath.(string),
				StartLine:  int(startLine.(int64)),
				EndLine:    int(endLine.(int64)),
				EntityType: entityType.(string),
				Name:       name.(string),
				Language:   language.(string),
			}
			
			if signature != nil {
				chunk.Signature = signature.(string)
			}
			
			// Save the score in the chunk
			chunk.Score = score.(float64)
			
			r.logger.Printf("Found chunk with score %f: %s\n", score.(float64), chunk.ID)
			chunks = append(chunks, chunk)
		}
		
		return chunks, nil
	})
	
	if err != nil {
		fmt.Printf("Neo4j search failed: %v\n", err)
		return nil, fmt.Errorf("search failed: %w", err)
	}
	
	chunks := result.([]CodeChunk)
	fmt.Printf("Search complete. Found %d matching chunks\n", len(chunks))
	return chunks, nil
}

// QueryLLM sends a query to the LLM with retrieved context
func (r *Neo4jRAG) QueryLLM(query string, maxTokens int) (string, error) {
	// First search for relevant code chunks
	chunks, err := r.SearchCode(query, 5)
	if err != nil {
		return "", fmt.Errorf("failed to search for relevant chunks: %w", err)
	}
	
	// Format prompt with context
	prompt := "Based on the following code snippets:\n\n"
	
	for i, chunk := range chunks {
		prompt += fmt.Sprintf("SNIPPET %d (%s, %s):\n```%s\n%s\n```\n\n",
			i+1, chunk.FilePath, chunk.EntityType, strings.ToLower(chunk.Language), chunk.Content)
	}
	
	prompt += fmt.Sprintf("Answer the following question: %s", query)
	
	r.logger.Println("Sending query to LLM")
	
	// Send to LLM
	req := LLMRequest{
		Prompt:      prompt,
		MaxTokens:   maxTokens,
		Temperature: 0.2,
	}
	
	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	
	// Call LLM server
	resp, err := http.Post(r.config.LLMServerURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	// Parse response
	var llmResp LLMResponse
	err = json.NewDecoder(resp.Body).Decode(&llmResp)
	if err != nil {
		return "", err
	}
	
	r.logger.Printf("LLM response received, tokens used: %d\n", llmResp.TokensUsed)
	
	return llmResp.Text, nil
}

// getLanguageFromExt gets the language name from file extension
func getLanguageFromExt(ext string) string {
	ext = strings.ToLower(ext)
	
	langMap := map[string]string{
		".go":   "Go",
		".py":   "Python",
		".js":   "JavaScript",
		".ts":   "TypeScript",
		".java": "Java",
		".c":    "C",
		".cpp":  "C++",
		".h":    "C/C++ Header",
		".hpp":  "C++ Header",
		".cs":   "C#",
		".php":  "PHP",
		".rb":   "Ruby",
		".rs":   "Rust",
		".swift": "Swift",
		".kt":   "Kotlin",
		".sh":   "Shell",
		".html": "HTML",
		".css":  "CSS",
		".sql":  "SQL",
		".md":   "Markdown",
	}
	
	if lang, ok := langMap[ext]; ok {
		return lang
	}
	
	return "Unknown"
}

// processQuery handles processing a query and displaying results
func processQuery(rag *Neo4jRAG, query string) {
	fmt.Println("\nQuery:", query)
	fmt.Println("\nSearching for relevant code...")
	
	// Auto-detect language filters from query
	languages := []string{}
	queryLower := strings.ToLower(query)
	
	languageKeywords := map[string]string{
		"golang":      "Go",
		"go code":     "Go",
		"python":      "Python",
		"py":          "Python",
		"javascript":  "JavaScript",
		"js":          "JavaScript",
		"typescript":  "TypeScript",
		"ts":          "TypeScript",
		"java":        "Java",
		"c#":          "C#",
		"csharp":      "C#",
		"c++":         "C++",
		"cpp":         "C++",
		"ruby":        "Ruby",
		"rust":        "Rust",
		"php":         "PHP",
		"swift":       "Swift",
		"kotlin":      "Kotlin",
		"scala":       "Scala",
		"shell":       "Shell",
		"bash":        "Shell",
		"sql":         "SQL",
	}
	
	// Check for language filters in the query
	for keyword, language := range languageKeywords {
		if strings.Contains(queryLower, keyword) {
			languages = append(languages, language)
		}
	}
	
	// Extract path filters from query
	pathFilters := []string{}
	pathPatterns := []string{
		"in directory", "in dir", "in folder", "in path",
		"from directory", "from dir", "from folder", "from path",
	}
	
	for _, pattern := range pathPatterns {
		if idx := strings.Index(queryLower, pattern); idx != -1 {
			// Extract the path after the pattern
			pathStart := idx + len(pattern)
			if pathStart < len(query) {
				pathText := query[pathStart:]
				// Find the end of the path (next punctuation or end of string)
				pathEnd := strings.IndexAny(pathText, ".,:;!?")
				if pathEnd == -1 {
					pathEnd = len(pathText)
				}
				
				if pathEnd > 0 {
					path := strings.Trim(pathText[:pathEnd], " \t\"'")
					if path != "" {
						// Add wildcard if needed
						if !strings.Contains(path, "*") {
							path = "*" + path + "*"
						}
						pathFilters = append(pathFilters, path)
					}
				}
			}
		}
	}
	
	// Log the search parameters
	if len(languages) > 0 {
		fmt.Printf("Language filters: %v\n", languages)
	}
	if len(pathFilters) > 0 {
		fmt.Printf("Path filters: %v\n", pathFilters)
	}
	
	// Use the advanced search
	chunks, err := rag.SearchCodeAdvanced(query, 5, languages, pathFilters, 0.1, true)
	if err != nil {
		fmt.Printf("Error searching for code: %v\n", err)
		return
	}
	
	// Display results with more context
	if len(chunks) == 0 {
		fmt.Println("No relevant code found")
	} else {
		fmt.Println("\nRelevant code chunks:")
		for i, chunk := range chunks {
			fmt.Printf("\n--- Chunk %d ---\n", i+1)
			
			// Display detailed file information with absolute path
			absPath, err := filepath.Abs(chunk.FilePath)
			if err != nil {
				absPath = chunk.FilePath // Fallback to relative path if absolute path fails
			}
			fmt.Printf("Absolute Path: %s\n", absPath)
			fmt.Printf("Relative Path: %s\n", chunk.FilePath)
			
			// Get directory and filename separately
			dir := filepath.Dir(absPath)
			filename := filepath.Base(absPath)
			fmt.Printf("Directory: %s\n", dir)
			fmt.Printf("Filename: %s\n", filename)
			
			// Display line range
			fmt.Printf("Lines: %d-%d\n", chunk.StartLine, chunk.EndLine)
			
			// Display entity information
			fmt.Printf("Type: %s", chunk.EntityType)
			if chunk.Name != "" {
				fmt.Printf(" - %s", chunk.Name)
			}
			
			// Display language
			if chunk.Language != "" {
				fmt.Printf("\nLanguage: %s", chunk.Language)
			}
			
			// Display signature if available
			if chunk.Signature != "" {
				fmt.Printf("\nSignature: %s", chunk.Signature)
			}
			
			fmt.Println("\n\nContent Preview:")
			
			// Print snippet of code (show more lines for better context)
			lines := strings.Split(chunk.Content, "\n")
			maxLines := 15 // Increased from 8 to 15 lines
			if len(lines) < maxLines {
				maxLines = len(lines)
			}
			for j := 0; j < maxLines; j++ {
				fmt.Printf("%d: %s\n", chunk.StartLine+j, lines[j])
			}
			if len(lines) > maxLines {
				fmt.Printf("... (%d more lines not shown)\n", len(lines) - maxLines)
			}
			
			// Add a separator between chunks
			fmt.Println("\n" + strings.Repeat("-", 80))
		}
	}
	
	// Generate answer using LLM
	fmt.Println("\nGenerating answer...")
	
	// Create a detailed summary of search results to include in the final answer
	searchResultsSummary := "\nSearch Results Summary:\n"
	for i, chunk := range chunks {
		absPath, _ := filepath.Abs(chunk.FilePath)
		
		// Add a separator line for better readability
		searchResultsSummary += fmt.Sprintf("\n%s\n", strings.Repeat("-", 80))
		
		// Add detailed file information
		searchResultsSummary += fmt.Sprintf("\n%d. MATCH DETAILS:\n", i+1)
		searchResultsSummary += fmt.Sprintf("   Similarity Score: %.6f\n", chunk.Score)
		searchResultsSummary += fmt.Sprintf("   Full Path: %s\n", absPath)
		searchResultsSummary += fmt.Sprintf("   Directory: %s\n", filepath.Dir(absPath))
		searchResultsSummary += fmt.Sprintf("   Filename: %s\n", filepath.Base(absPath))
		searchResultsSummary += fmt.Sprintf("   Lines: %d-%d\n", chunk.StartLine, chunk.EndLine)
		
		// Add entity information if available
		if chunk.EntityType != "" {
			searchResultsSummary += fmt.Sprintf("   Type: %s\n", chunk.EntityType)
		}
		if chunk.Name != "" {
			searchResultsSummary += fmt.Sprintf("   Name: %s\n", chunk.Name)
		}
		if chunk.Language != "" {
			searchResultsSummary += fmt.Sprintf("   Language: %s\n", chunk.Language)
		}
		
		// Add a snippet of the content
		lines := strings.Split(chunk.Content, "\n")
		previewLines := 5
		if len(lines) < previewLines {
			previewLines = len(lines)
		}
		
		searchResultsSummary += "\n   Content Preview:\n"
		for j := 0; j < previewLines; j++ {
			searchResultsSummary += fmt.Sprintf("   %d: %s\n", chunk.StartLine+j, lines[j])
		}
		if len(lines) > previewLines {
			searchResultsSummary += fmt.Sprintf("   ... (%d more lines)\n", len(lines) - previewLines)
		}
	}
	
	// Get answer from LLM
	answer, err := rag.QueryLLM(query, 1000)
	if err != nil {
		fmt.Printf("Error generating answer: %v\n", err)
		return
	}

	// Display final answer with search results included
	fmt.Println("\n--- Answer ---")
	fmt.Println(searchResultsSummary)
	if answer != "" {
		fmt.Println("\nLLM Response:")
		fmt.Println(answer)
	}
}
// extractKeywords extracts important keywords from a query string
func extractKeywords(query string) []string {
	// Split the query into words
	words := strings.Fields(strings.ToLower(query))
	
	// Filter out common stop words
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
		"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
		"being": true, "have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "to": true, "from": true, "in": true, "out": true, "on": true,
		"off": true, "over": true, "under": true, "again": true, "further": true,
		"then": true, "once": true, "here": true, "there": true, "when": true,
		"where": true, "why": true, "how": true, "all": true, "any": true, "both": true,
		"each": true, "few": true, "more": true, "most": true, "other": true, "some": true,
		"such": true, "no": true, "nor": true, "not": true, "only": true, "own": true,
		"same": true, "so": true, "than": true, "too": true, "very": true, "can": true,
		"will": true, "just": true, "should": true, "now": true,
	}
	
	keywords := []string{}
	for _, word := range words {
		// Remove punctuation
		word = strings.Trim(word, ".,;:!?()[]{}-\"'`")
		
		// Skip empty words, stop words, and single characters
		if word == "" || stopWords[word] || len(word) <= 1 {
			continue
		}
		
		keywords = append(keywords, word)
	}
	
	return keywords
}

// globToRegex converts a glob pattern to a regex pattern
func globToRegex(pattern string) string {
	// Escape special regex characters
	regex := regexp.QuoteMeta(pattern)
	
	// Convert glob wildcards to regex wildcards
	regex = strings.ReplaceAll(regex, "\\*", ".*")
	regex = strings.ReplaceAll(regex, "\\?", ".")
	
	// Add start and end anchors
	regex = "^" + regex + "$"
	
	return regex
}

func main() {
	// Parse command line flags
	neo4jURI := flag.String("neo4j-uri", "bolt://localhost:7687", "Neo4j URI")
	neo4jUser := flag.String("neo4j-user", "neo4j", "Neo4j username")
	neo4jPassword := flag.String("neo4j-password", "password", "Neo4j password")
	embeddingURL := flag.String("embedding-url", "http://localhost:8080/embeddings", "URL for embedding service")
	llmURL := flag.String("llm-url", "http://localhost:8081/completion", "URL for LLM service")
	maxChunkSize := flag.Int("max-chunk-size", 1000, "Maximum chunk size in characters")
	chunkOverlap := flag.Int("chunk-overlap", 100, "Chunk overlap in characters")
	codeDir := flag.String("code-dir", "", "Directory to index")
	dbName := flag.String("db-name", "coderag", "Database name")
	
	indexCmd := flag.Bool("index", false, "Index code directory")
	queryCmd := flag.Bool("query", false, "Query the system")
	queryString := flag.String("query-string", "", "Query string to search for (used with --query)")
	
	flag.Parse()
	
	// Configure the RAG system
	config := Config{
		Neo4jURI:      *neo4jURI,
		Neo4jUser:     *neo4jUser,
		Neo4jPassword: *neo4jPassword,
		EmbeddingURL:  *embeddingURL,
		LLMServerURL:  *llmURL,
		MaxChunkSize:  *maxChunkSize,
		ChunkOverlap:  *chunkOverlap,
		CodeDir:       *codeDir,
		DbName:        *dbName,
	}
	
	// Create the Neo4j RAG instance
	rag, err := NewNeo4jRAG(config)
	if err != nil {
		log.Fatalf("Failed to initialize Neo4j RAG: %v", err)
	}
	defer rag.Close()
	
	// Handle commands
	if *indexCmd {
		if *codeDir == "" {
			log.Fatal("Please specify a directory to index with --code-dir")
		}
		
		fmt.Printf("Indexing directory: %s\n", *codeDir)
		err := rag.IndexDirectory(*codeDir)
		if err != nil {
			log.Fatalf("Failed to index directory: %v", err)
		}
		
		fmt.Println("Indexing complete")
	} else if *queryCmd {
		// Check if query string was provided as argument
		if *queryString != "" {
			// Use the provided query string directly
			query := *queryString
			fmt.Printf("\nQuery: %s\n", query)
			
			// Process the query
			processQuery(rag, query)
		} else {
			// Start interactive query mode
			reader := bufio.NewReader(os.Stdin)
			
			for {
				fmt.Print("\nEnter your query (or 'exit' to quit): ")
				query, _ := reader.ReadString('\n')
				query = strings.TrimSpace(query)
				
				if query == "exit" {
					break
				}
				
				if query == "" {
					continue
				}
				
				// Process the query
				processQuery(rag, query)
			}
		}
	} else {
		// No command specified, print usage
		fmt.Println("Local RAG System with Neo4j and LMStudio")
		fmt.Println("\nUsage:")
		fmt.Println("  To index code:   go run main.go --index --code-dir=/path/to/code")
		fmt.Println("  To query:        go run main.go --query")
		fmt.Println("  To query directly: go run main.go --query --query-string=\"your query here\"")
		fmt.Println("\nOptions:")
		flag.PrintDefaults()
	}
}
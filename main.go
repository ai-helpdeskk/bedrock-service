package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "strings"
    "time"

    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/credentials"
    "github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
    "github.com/gorilla/mux"
)

// Request and Response structs
type GenerateRequest struct {
    Prompt      string  `json:"prompt"`
    MaxTokens   int     `json:"max_tokens,omitempty"`
    Temperature float64 `json:"temperature,omitempty"`
    Model       string  `json:"model,omitempty"`
}

type GenerateResponse struct {
    Response   string `json:"response"`
    ModelUsed  string `json:"model_used"`
    TokenCount int    `json:"token_count,omitempty"`
}

type HealthResponse struct {
    Status         string   `json:"status"`
    Service        string   `json:"service"`
    AvailableModels []string `json:"available_models"`
}

type ModelInfo struct {
    ID          string
    Name        string
    Available   bool
    MessageAPI  bool // Uses new message API format
}

// BedrockClient wraps the AWS Bedrock client
type BedrockClient struct {
    client         *bedrockruntime.Client
    availableModels []ModelInfo
}

// NewBedrockClient creates a new Bedrock client
func NewBedrockClient() (*BedrockClient, error) {
    // Get AWS credentials from environment variables
    awsAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
    awsSecretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
    awsRegion := os.Getenv("AWS_REGION")
    
    if awsRegion == "" {
        awsRegion = "us-east-1" // Default region
    }

    // Create AWS config
    cfg, err := config.LoadDefaultConfig(context.TODO(),
        config.WithRegion(awsRegion),
        config.WithCredentialsProvider(
            credentials.NewStaticCredentialsProvider(awsAccessKey, awsSecretKey, ""),
        ),
    )
    if err != nil {
        return nil, fmt.Errorf("unable to load SDK config: %v", err)
    }

    // Create Bedrock client
    client := bedrockruntime.NewFromConfig(cfg)
    
    // Define available models with enhanced context handling
    availableModels := []ModelInfo{
        // Claude 3.5 models (best for conversation memory)
        {ID: "anthropic.claude-3-5-sonnet-20241022-v2:0", Name: "Claude 3.5 Sonnet v2", MessageAPI: true},
        {ID: "anthropic.claude-3-5-sonnet-20240620-v1:0", Name: "Claude 3.5 Sonnet", MessageAPI: true},
        {ID: "anthropic.claude-3-5-haiku-20241022-v1:0", Name: "Claude 3.5 Haiku", MessageAPI: true},
        
        // Claude 3 models
        {ID: "anthropic.claude-3-sonnet-20240229-v1:0", Name: "Claude 3 Sonnet", MessageAPI: true},
        {ID: "anthropic.claude-3-haiku-20240307-v1:0", Name: "Claude 3 Haiku", MessageAPI: true},
        {ID: "anthropic.claude-3-opus-20240229-v1:0", Name: "Claude 3 Opus", MessageAPI: true},
        
        // Older Claude models (fallback)
        {ID: "anthropic.claude-v2:1", Name: "Claude v2.1", MessageAPI: false},
        {ID: "anthropic.claude-v2", Name: "Claude v2", MessageAPI: false},
        {ID: "anthropic.claude-instant-v1", Name: "Claude Instant", MessageAPI: false},
    }
    
    return &BedrockClient{
        client: client,
        availableModels: availableModels,
    }, nil
}

// TestModelAvailability tests which models are actually available
func (bc *BedrockClient) TestModelAvailability() {
    log.Println("Testing model availability...")
    
    testPrompt := "Hello"
    
    for i := range bc.availableModels {
        model := &bc.availableModels[i]
        
        var requestBody map[string]interface{}
        
        if model.MessageAPI {
            // New message API format for Claude 3+ models
            requestBody = map[string]interface{}{
                "anthropic_version": "bedrock-2023-05-31",
                "max_tokens": 10,
                "messages": []map[string]string{
                    {
                        "role": "user",
                        "content": testPrompt,
                    },
                },
            }
        } else {
            // Legacy format for Claude v2 and earlier
            requestBody = map[string]interface{}{
                "prompt": fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", testPrompt),
                "max_tokens_to_sample": 10,
            }
        }

        bodyBytes, _ := json.Marshal(requestBody)
        
        _, err := bc.client.InvokeModel(context.TODO(), &bedrockruntime.InvokeModelInput{
            Body:        bodyBytes,
            ModelId:     aws.String(model.ID),
            ContentType: aws.String("application/json"),
        })
        
        if err != nil {
            log.Printf("Model %s (%s): UNAVAILABLE - %v", model.Name, model.ID, err)
            model.Available = false
        } else {
            log.Printf("Model %s (%s): AVAILABLE ✓", model.Name, model.ID)
            model.Available = true
        }
    }
}

// GetAvailableModels returns list of available model names
func (bc *BedrockClient) GetAvailableModels() []string {
    var available []string
    for _, model := range bc.availableModels {
        if model.Available {
            available = append(available, model.Name)
        }
    }
    return available
}

// GenerateText calls Amazon Bedrock with enhanced context handling
func (bc *BedrockClient) GenerateText(prompt string, preferredModel string, maxTokens int, temperature float64) (string, string, error) {
    // Set defaults
    if maxTokens == 0 {
        maxTokens = 2000 // Increased for better responses with context
    }
    if temperature == 0 {
        temperature = 0.7
    }

    // Find preferred model if specified
    var modelsToTry []ModelInfo
    if preferredModel != "" {
        for _, model := range bc.availableModels {
            if model.Available && (strings.Contains(strings.ToLower(model.Name), strings.ToLower(preferredModel)) || 
                                 strings.Contains(strings.ToLower(model.ID), strings.ToLower(preferredModel))) {
                modelsToTry = append(modelsToTry, model)
                break
            }
        }
    }
    
    // Add all available models as fallback
    for _, model := range bc.availableModels {
        if model.Available {
            // Check if already added
            found := false
            for _, existing := range modelsToTry {
                if existing.ID == model.ID {
                    found = true
                    break
                }
            }
            if !found {
                modelsToTry = append(modelsToTry, model)
            }
        }
    }
    
    if len(modelsToTry) == 0 {
        return "", "", fmt.Errorf("no available models found")
    }
    
    var lastError error
    for _, model := range modelsToTry {
        log.Printf("Trying model: %s (%s)", model.Name, model.ID)
        
        var requestBody map[string]interface{}
        
        if model.MessageAPI {
            // Enhanced system prompt for better context understanding
            systemPrompt := "You are a helpful AI assistant with access to conversation history and uploaded files. " +
                           "When responding, consider the full context provided, including previous conversations and any file content. " +
                           "If file content is mentioned in the context, analyze and reference it appropriately in your response. " +
                           "Be conversational, helpful, and maintain continuity with previous interactions."
            
            requestBody = map[string]interface{}{
                "anthropic_version": "bedrock-2023-05-31",
                "max_tokens": maxTokens,
                "system": systemPrompt,
                "messages": []map[string]interface{}{
                    {
                        "role": "user",
                        "content": prompt,
                    },
                },
                "temperature": temperature,
            }
        } else {
            // Enhanced legacy format with better context handling
            enhancedPrompt := fmt.Sprintf("\n\nHuman: You are a helpful AI assistant with conversation memory and file analysis capabilities. Please provide thoughtful, contextual responses based on the information provided.\n\n%s\n\nAssistant:", prompt)
            
            requestBody = map[string]interface{}{
                "prompt": enhancedPrompt,
                "max_tokens_to_sample": maxTokens,
                "temperature": temperature,
            }
        }

        bodyBytes, err := json.Marshal(requestBody)
        if err != nil {
            lastError = fmt.Errorf("error marshaling request: %v", err)
            continue
        }

        // Invoke the model
        resp, err := bc.client.InvokeModel(context.TODO(), &bedrockruntime.InvokeModelInput{
            Body:        bodyBytes,
            ModelId:     aws.String(model.ID),
            ContentType: aws.String("application/json"),
        })
        
        if err != nil {
            lastError = err
            log.Printf("Error with model %s: %v", model.Name, err)
            continue
        }

        // Parse the response
        var response map[string]interface{}
        if err := json.Unmarshal(resp.Body, &response); err != nil {
            lastError = fmt.Errorf("error parsing response: %v", err)
            continue
        }

        // Extract text based on API format
        if model.MessageAPI {
            // New message API format
            if content, ok := response["content"].([]interface{}); ok && len(content) > 0 {
                if firstContent, ok := content[0].(map[string]interface{}); ok {
                    if text, ok := firstContent["text"].(string); ok {
                        log.Printf("✓ Successfully used model: %s", model.Name)
                        return text, model.Name, nil
                    }
                }
            }
        } else {
            // Legacy format
            if completion, ok := response["completion"].(string); ok {
                log.Printf("✓ Successfully used model: %s", model.Name)
                return completion, model.Name, nil
            }
        }
        
        lastError = fmt.Errorf("unexpected response format from model %s", model.Name)
    }

    return "", "", fmt.Errorf("all available models failed. Last error: %v", lastError)
}

// Handlers
func healthHandler(bc *BedrockClient) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        response := HealthResponse{
            Status:          "healthy",
            Service:         "bedrock-service",
            AvailableModels: bc.GetAvailableModels(),
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(response)
    }
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
    response := map[string]string{
        "message": "Enhanced Bedrock Service is running",
        "version": "3.0.0",
        "features": "conversation-context, file-analysis, multi-model-support",
        "documentation": "POST /generate with {\"prompt\": \"your prompt with context\", \"model\": \"optional model preference\"}",
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}

func generateHandler(bc *BedrockClient) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var req GenerateRequest
        
        // Parse request body
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            http.Error(w, "Invalid request body", http.StatusBadRequest)
            return
        }

        // Validate prompt
        if req.Prompt == "" {
            http.Error(w, "Prompt is required", http.StatusBadRequest)
            return
        }

        log.Printf("Received enhanced prompt: %s (model preference: %s)", 
            req.Prompt[:min(100, len(req.Prompt))], req.Model)

        // Generate text using Bedrock with enhanced context
        response, modelUsed, err := bc.GenerateText(req.Prompt, req.Model, req.MaxTokens, req.Temperature)
        if err != nil {
            log.Printf("Error generating text: %v", err)
            http.Error(w, fmt.Sprintf("Error generating response: %v", err), http.StatusInternalServerError)
            return
        }

        // Send response
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(GenerateResponse{
            Response:  response,
            ModelUsed: modelUsed,
        })
    }
}

func modelsHandler(bc *BedrockClient) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        models := make([]map[string]interface{}, 0)
        for _, model := range bc.availableModels {
            models = append(models, map[string]interface{}{
                "id":        model.ID,
                "name":      model.Name,
                "available": model.Available,
                "api_type":  map[bool]string{true: "messages", false: "legacy"}[model.MessageAPI],
                "features":  []string{"conversation-context", "file-analysis"},
            })
        }
        
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]interface{}{
            "models": models,
        })
    }
}

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}

func main() {
    log.Println("Starting Enhanced Bedrock Service v3.0...")
    
    // Initialize Bedrock client
    bc, err := NewBedrockClient()
    if err != nil {
        log.Fatalf("Failed to initialize Bedrock client: %v", err)
    }

    // Test model availability
    bc.TestModelAvailability()

    // Create router
    router := mux.NewRouter()
    
    // Register routes
    router.HandleFunc("/", rootHandler).Methods("GET")
    router.HandleFunc("/health", healthHandler(bc)).Methods("GET")
    router.HandleFunc("/models", modelsHandler(bc)).Methods("GET")
    router.HandleFunc("/generate", generateHandler(bc)).Methods("POST")

    // Configure server with enhanced timeouts for context processing
    srv := &http.Server{
        Handler:      router,
        Addr:         ":9000",
        WriteTimeout: 120 * time.Second,  // Increased for context processing
        ReadTimeout:  60 * time.Second,   // Increased for large context
    }

    log.Printf("Enhanced Bedrock Service started on port 9000 with %d available models", len(bc.GetAvailableModels()))
    log.Println("Features: Conversation Context, File Analysis, Multi-Model Support")
    
    if err := srv.ListenAndServe(); err != nil {
        log.Fatal(err)
    }
}

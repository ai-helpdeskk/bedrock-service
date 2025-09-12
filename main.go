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
    MessageAPI  bool
}

type BedrockClient struct {
    client         *bedrockruntime.Client
    availableModels []ModelInfo
}

func NewBedrockClient() (*BedrockClient, error) {
    awsAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
    awsSecretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
    awsRegion := os.Getenv("AWS_REGION")
    
    if awsRegion == "" {
        awsRegion = "us-east-1"
    }

    if awsAccessKey == "" || awsSecretKey == "" {
        return nil, fmt.Errorf("AWS credentials not provided")
    }

    cfg, err := config.LoadDefaultConfig(context.TODO(),
        config.WithRegion(awsRegion),
        config.WithCredentialsProvider(
            credentials.NewStaticCredentialsProvider(awsAccessKey, awsSecretKey, ""),
        ),
    )
    if err != nil {
        return nil, fmt.Errorf("unable to load SDK config: %v", err)
    }

    client := bedrockruntime.NewFromConfig(cfg)
    
    availableModels := []ModelInfo{
        {ID: "anthropic.claude-3-5-sonnet-20241022-v2:0", Name: "Claude 3.5 Sonnet v2", MessageAPI: true},
        {ID: "anthropic.claude-3-5-sonnet-20240620-v1:0", Name: "Claude 3.5 Sonnet", MessageAPI: true},
        {ID: "anthropic.claude-3-5-haiku-20241022-v1:0", Name: "Claude 3.5 Haiku", MessageAPI: true},
        {ID: "anthropic.claude-3-sonnet-20240229-v1:0", Name: "Claude 3 Sonnet", MessageAPI: true},
        {ID: "anthropic.claude-3-haiku-20240307-v1:0", Name: "Claude 3 Haiku", MessageAPI: true},
        {ID: "anthropic.claude-v2:1", Name: "Claude v2.1", MessageAPI: false},
        {ID: "anthropic.claude-v2", Name: "Claude v2", MessageAPI: false},
    }
    
    return &BedrockClient{
        client: client,
        availableModels: availableModels,
    }, nil
}

func (bc *BedrockClient) TestModelAvailability() {
    log.Println("Testing model availability...")
    
    testPrompt := "Hello"
    
    for i := range bc.availableModels {
        model := &bc.availableModels[i]
        
        var requestBody map[string]interface{}
        
        if model.MessageAPI {
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
            requestBody = map[string]interface{}{
                "prompt": fmt.Sprintf("\n\nHuman: %s\n\nAssistant:", testPrompt),
                "max_tokens_to_sample": 10,
            }
        }

        bodyBytes, _ := json.Marshal(requestBody)
        
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        _, err := bc.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
            Body:        bodyBytes,
            ModelId:     aws.String(model.ID),
            ContentType: aws.String("application/json"),
        })
        cancel()
        
        if err != nil {
            log.Printf("Model %s (%s): UNAVAILABLE - %v", model.Name, model.ID, err)
            model.Available = false
        } else {
            log.Printf("Model %s (%s): AVAILABLE ✓", model.Name, model.ID)
            model.Available = true
        }
    }
}

func (bc *BedrockClient) GetAvailableModels() []string {
    var available []string
    for _, model := range bc.availableModels {
        if model.Available {
            available = append(available, model.Name)
        }
    }
    return available
}

func (bc *BedrockClient) GenerateText(prompt string, preferredModel string, maxTokens int, temperature float64) (string, string, error) {
    if maxTokens == 0 {
        maxTokens = 2000
    }
    if temperature == 0 {
        temperature = 0.7
    }

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
    
    for _, model := range bc.availableModels {
        if model.Available {
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

        ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
        resp, err := bc.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
            Body:        bodyBytes,
            ModelId:     aws.String(model.ID),
            ContentType: aws.String("application/json"),
        })
        cancel()
        
        if err != nil {
            lastError = err
            log.Printf("Error with model %s: %v", model.Name, err)
            continue
        }

        var response map[string]interface{}
        if err := json.Unmarshal(resp.Body, &response); err != nil {
            lastError = fmt.Errorf("error parsing response: %v", err)
            continue
        }

        if model.MessageAPI {
            if content, ok := response["content"].([]interface{}); ok && len(content) > 0 {
                if firstContent, ok := content[0].(map[string]interface{}); ok {
                    if text, ok := firstContent["text"].(string); ok {
                        log.Printf("✓ Successfully used model: %s", model.Name)
                        return text, model.Name, nil
                    }
                }
            }
        } else {
            if completion, ok := response["completion"].(string); ok {
                log.Printf("✓ Successfully used model: %s", model.Name)
                return completion, model.Name, nil
            }
        }
        
        lastError = fmt.Errorf("unexpected response format from model %s", model.Name)
    }

    return "", "", fmt.Errorf("all available models failed. Last error: %v", lastError)
}

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
        "message": "Bedrock Service is running",
        "version": "1.0.0",
        "features": "conversation-context, file-analysis, multi-model-support",
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}

func generateHandler(bc *BedrockClient) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var req GenerateRequest
        
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            http.Error(w, "Invalid request body", http.StatusBadRequest)
            return
        }

        if req.Prompt == "" {
            http.Error(w, "Prompt is required", http.StatusBadRequest)
            return
        }

        log.Printf("Received prompt: %s", req.Prompt[:min(100, len(req.Prompt))])

        response, modelUsed, err := bc.GenerateText(req.Prompt, req.Model, req.MaxTokens, req.Temperature)
        if err != nil {
            log.Printf("Error generating text: %v", err)
            http.Error(w, fmt.Sprintf("Error generating response: %v", err), http.StatusInternalServerError)
            return
        }

        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(GenerateResponse{
            Response:  response,
            ModelUsed: modelUsed,
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
    log.Println("Starting Bedrock Service...")
    
    bc, err := NewBedrockClient()
    if err != nil {
        log.Fatalf("Failed to initialize Bedrock client: %v", err)
    }

    bc.TestModelAvailability()

    router := mux.NewRouter()
    
    router.HandleFunc("/", rootHandler).Methods("GET")
    router.HandleFunc("/health", healthHandler(bc)).Methods("GET")
    router.HandleFunc("/generate", generateHandler(bc)).Methods("POST")

    srv := &http.Server{
        Handler:      router,
        Addr:         ":9000",
        WriteTimeout: 120 * time.Second,
        ReadTimeout:  60 * time.Second,
    }

    log.Printf("Bedrock Service started on port 9000")
    
    if err := srv.ListenAndServe(); err != nil {
        log.Fatal(err)
    }
}

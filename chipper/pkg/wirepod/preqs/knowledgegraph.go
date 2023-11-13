package processreqs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	 "fmt"
   	 "sync"
	pb "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/kercre123/chipper/pkg/logger"
	"github.com/kercre123/chipper/pkg/vars"
	"github.com/kercre123/chipper/pkg/vtt"
	sr "github.com/kercre123/chipper/pkg/wirepod/speechrequest"
	"github.com/pkg/errors"
	"github.com/soundhound/houndify-sdk-go"
)

var HKGclient houndify.Client
var HoundEnable bool = true

func ParseSpokenResponse(serverResponseJSON string) (string, error) {
	result := make(map[string]interface{})
	err := json.Unmarshal([]byte(serverResponseJSON), &result)
	if err != nil {
		logger.Println(err.Error())
		return "", errors.New("failed to decode json")
	}
	if !strings.EqualFold(result["Status"].(string), "OK") {
		return "", errors.New(result["ErrorMessage"].(string))
	}
	if result["NumToReturn"].(float64) < 1 {
		return "", errors.New("no results to return")
	}
	return result["AllResults"].([]interface{})[0].(map[string]interface{})["SpokenResponseLong"].(string), nil
}

func InitKnowledge() {
	if vars.APIConfig.Knowledge.Enable && vars.APIConfig.Knowledge.Provider == "houndify" {
		if vars.APIConfig.Knowledge.ID == "" || vars.APIConfig.Knowledge.Key == "" {
			vars.APIConfig.Knowledge.Enable = false
			logger.Println("Houndify Client Key or ID was empty, not initializing kg client")
		} else {
			HKGclient = houndify.Client{
				ClientID:  vars.APIConfig.Knowledge.ID,
				ClientKey: vars.APIConfig.Knowledge.Key,
			}
			HKGclient.EnableConversationState()
			logger.Println("Initialized Houndify client")
		}
	}
}

var NoResult string = "NoResultCommand"
var NoResultSpoken string

func houndifyKG(req sr.SpeechRequest) string {
	var apiResponse string
	if vars.APIConfig.Knowledge.Enable && vars.APIConfig.Knowledge.Provider == "houndify" {
		logger.Println("Sending request to Houndify...")
		serverResponse := StreamAudioToHoundify(req, HKGclient)
		apiResponse, _ = ParseSpokenResponse(serverResponse)
		logger.Println("Houndify response: " + apiResponse)
	} else {
		apiResponse = "Houndify is not enabled."
		logger.Println("Houndify is not enabled.")
	}
	return apiResponse
}

func togetherRequest(transcribedText string) string {
	sendString := "You are a helpful robot called Vector . You will be given a question asked by a user and you must provide the best answer you can. It may not be punctuated or spelled correctly. Keep the answer concise yet informative. Here is the question: " + "\\" + "\"" + transcribedText + "\\" + "\"" + " , Answer: "
	url := "https://api.together.xyz/inference"
	model := vars.APIConfig.Knowledge.Model
	formData := `{
"model": "` + model + `",
"prompt": "` + sendString + `",
"temperature": 0.7,
"max_tokens": 256,
"top_p": 1
}`
	logger.Println("Making request to Together API...")
	logger.Println("Model is " + model)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer([]byte(formData)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+vars.APIConfig.Knowledge.Key)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "There was an error making the request to Together API"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var togetherResponse map[string]any
	err = json.Unmarshal(body, &togetherResponse)
	if err != nil {
		return "Together API returned no response."
	}
	output := togetherResponse["output"].(map[string]any)
	choice := output["choices"].([]any)
	for _, val := range choice {
		x := val.(map[string]any)
		textResponse := x["text"].(string)
		apiResponse := strings.TrimSuffix(textResponse, "</s>")
		logger.Println("Together response: " + apiResponse)
		return apiResponse
	}
	// In case text is not present in result from API, return a string saying answer was not found
	return "Answer was not found"
}

func openaiRequest(transcribedText string) string {
	sendString := "You are a helpful robot called " + vars.APIConfig.Knowledge.RobotName + ". You will be given a question asked by a user and you must provide the best answer you can. It may not be punctuated or spelled correctly as the STT model is small. The answer will be put through TTS, so it should be a speakable string. Keep the answer concise yet informative. Here is the question: " + "\\" + "\"" + transcribedText + "\\" + "\"" + " , Answer: "
	logger.Println("Making request to OpenAI...")
	url := "https://api.openai.com/v1/completions"
	formData := `{
"model": "gpt-3.5-turbo-instruct",
"prompt": "` + sendString + `",
"temperature": 0.7,
"max_tokens": 256,
"top_p": 1,
"frequency_penalty": 0.2,
"presence_penalty": 0
}`
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer([]byte(formData)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+vars.APIConfig.Knowledge.Key)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.Println(err)
		return "There was an error making the request to OpenAI."
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	type openAIStruct struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int    `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Text         string      `json:"text"`
			Index        int         `json:"index"`
			Logprobs     interface{} `json:"logprobs"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	var openAIResponse openAIStruct
	err = json.Unmarshal(body, &openAIResponse)
	if err != nil || len(openAIResponse.Choices) == 0 {
		logger.Println("OpenAI returned no response.")
		return "OpenAI returned no response."
	}
	apiResponse := strings.TrimSpace(openAIResponse.Choices[0].Text)
	logger.Println("OpenAI response: " + apiResponse)
	return apiResponse
}

func openaiKG(speechReq sr.SpeechRequest) string {
	transcribedText, err := sttHandler(speechReq)
	if err != nil {
		return "There was an error."
	}
	return openaiRequest(transcribedText)
}

func togetherKG(speechReq sr.SpeechRequest) string {
	transcribedText, err := sttHandler(speechReq)
	if err != nil {
		return "There was an error."
	}
	return togetherRequest(transcribedText)
}

// Takes a SpeechRequest, figures out knowledgegraph provider, makes request, returns API response
func KgRequest(speechReq sr.SpeechRequest) string {
	if vars.APIConfig.Knowledge.Enable {
		if vars.APIConfig.Knowledge.Provider == "houndify" {
			return houndifyKG(speechReq)
		} else if vars.APIConfig.Knowledge.Provider == "openai" {
			return openaiKG(speechReq)
		} else if vars.APIConfig.Knowledge.Provider == "together" {
			return togetherKG(speechReq)
		}
	}
	return "Knowledge graph is not enabled. This can be enabled in the web interface."
}

func (s *Server) ProcessKnowledgeGraph(req *vtt.KnowledgeGraphRequest) (*vtt.KnowledgeGraphResponse, error) {
	// Entree
	InitKnowledge()
	speechReq := sr.ReqToSpeechRequest(req)
	apiResponse := KgRequest(speechReq)
	kg := pb.KnowledgeGraphResponse{
		Session:     req.Session,
		DeviceId:    req.Device,
		CommandType: NoResult,
		SpokenText:  apiResponse,
	}
	logger.Println("(KG) Bot " + speechReq.Device + " request served.")

	// Use of Vector SDK for sending the TTS

	// Créer un WaitGroup pour attendre la fin de toutes les goroutines
	var wg sync.WaitGroup

	// Créer un canal pour signaler le succès de chaque goroutine
	successCh := make(chan bool, 3)

	// Lancer les 3 requêtes de manière asynchrone (en parallèle)
	wg.Add(3)
	logger.Println("Sending the 3 request to setup the TTS")
	go sendRequest("http://escapepod.local/api-sdk/assume_behavior_control?priority=high&serial="+speechReq.Device , &wg, successCh)
	go sendRequest("http://escapepod.local/api-sdk/say_text?text=" + apiResponse + "serial=" + speechReq.Device ,  &wg, successCh)
	go sendRequest("http://escapepod.local/api-sdk/release_behavior_control?serial="+speechReq.Device , &wg, successCh)
	logger.Println("Ending of the TTS routine")
	
	// Attendre que toutes les goroutines se terminent
	wg.Wait()

	// Fermer le canal après que toutes les goroutines ont terminé
	close(successCh)

	// Vérifier si les trois requêtes ont réussi
	for success := range successCh {
		if !success {
			// Si l'une des requêtes a échoué, lancez une requête synchrone et renvoyez une erreur
			if err := retryRequest("http://escapepod.local/api-sdk/release_behavior_control?serial="+speechReq.Device); err != nil {
				return nil, fmt.Errorf("Une ou plusieurs requêtes ont échoué et la requête de réessai a également échoué: %v", err)
			}
			break
		}
	}

	// Envoyer la réponse du knowledge graph
	if err := req.Stream.Send(&kg); err != nil {
		return nil, err
	}
	return nil, nil
}

func sendRequest(url string, wg *sync.WaitGroup, successCh chan bool) {
	defer wg.Done()

	// Effectuer la requête GET
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("Erreur lors de la requête GET:", err)
		// Signal d'échec à travers le canal
		successCh <- false
		return
	}
	defer resp.Body.Close()

	// Traitement de la réponse (remplacez cela par votre propre logique de traitement)
	logger.Println("Réponse pour %s : Code %d\n", url, resp.StatusCode)

	// Signal de succès à travers le canal
	successCh <- true
}

func retryRequest(url string) error {
	// Effectuer la requête GET de réessai de manière synchrone
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("La requête de réessai a échoué: %v", err)
	}
	defer resp.Body.Close()

	// Traitement de la réponse de réessai (remplacez cela par votre propre logique de traitement)
	logger.Println("Réponse pour %s (retry) : Code %d\n", url, resp.StatusCode)

	return nil
}


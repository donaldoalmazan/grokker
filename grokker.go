package grokker

import (
	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"math"
	"os"
	"sort"
	"strings"

	. "github.com/stevegt/goadapt"

	"github.com/fabiustech/openai"
	"github.com/fabiustech/openai/models"
	oai "github.com/sashabaranov/go-openai"
)

// Grokker is a library for analyzing a set of documents and asking
// questions about them using the OpenAI chat and embeddings APIs.
//
// It uses this algorithm (generated by ChatGPT):
//
// To use embeddings in conjunction with the OpenAI Chat API to
// analyze a document, you can follow these general steps:
//
// (1) Break up the document into smaller text chunks or passages,
// each with a length of up to 8192 tokens (the maximum input size for
// the text-embedding-ada-002 model used by the Embeddings API).
//
// (2) For each text chunk, generate an embedding using the
// openai.Embedding.create() function. Store the embeddings for each
// chunk in a data structure such as a list or dictionary.
//
// (3) Use the Chat API to ask questions about the document. To do
// this, you can use the openai.Completion.create() function,
// providing the text of the previous conversation as the prompt
// parameter.
//
// (4) When a question is asked, use the embeddings of the document
// chunks to find the most relevant passages for the question. You can
// use a similarity measure such as cosine similarity to compare the
// embeddings of the question and each document chunk, and return the
// chunks with the highest similarity scores.
//
// (5) Provide the most relevant document chunks to the
// openai.Completion.create() function as additional context for
// generating a response. This will allow the model to better
// understand the context of the question and generate a more relevant
// response.
//
// Repeat steps 3-5 for each question asked, updating the conversation
// prompt as needed.

// Document is a single document in a document repository.
type Document struct {
	// The path to the document file.
	Path string
}

// Chunk is a single chunk of text from a document.
type Chunk struct {
	// The document that this chunk is from.
	// XXX this is redundant; we could just use the document's path.
	// XXX a chunk should be able to be from multiple documents.
	Document *Document
	// The text of the chunk.
	Text string
	// The embedding of the chunk.
	Embedding []float64
}

type Grokker struct {
	embeddingClient *openai.Client
	chatClient      *oai.Client
	// The root directory of the document repository.
	Root string
	// The list of documents in the database.
	Documents []*Document
	// The list of chunks in the database.
	Chunks []*Chunk
	// The maximum number of tokens to use for each document chunk.
	MaxChunkSize int
	// Approximate number of characters per token.
	// XXX replace with a real tokenizer.
	CharsPerToken float64
}

// New creates a new Grokker database.
func New() *Grokker {
	g := &Grokker{
		MaxChunkSize: 4096 * 4.0,
		// XXX replace with a real tokenizer.
		CharsPerToken: 3.5,
	}
	token := os.Getenv("OPENAI_API_KEY")
	g.embeddingClient = openai.NewClient(token)
	g.chatClient = oai.NewClient(token)
	return g
}

// Load loads a Grokker database from an io.Reader.
func Load(r io.Reader) (g *Grokker, err error) {
	buf, err := ioutil.ReadAll(r)
	Ck(err)
	g = New()
	err = json.Unmarshal(buf, g)
	return
}

// Save saves a Grokker database as json data in an io.Writer.
func (g *Grokker) Save(w io.Writer) (err error) {
	data, err := json.Marshal(g)
	Ck(err)
	_, err = w.Write(data)
	return
}

// UpdateEmbeddings updates the embeddings for any documents that have
// changed since the last time the embeddings were updated.  It returns
// true if any embeddings were updated.
func (g *Grokker) UpdateEmbeddings(grokfn string) (update bool, err error) {
	defer Return(&err)
	// we use the timestamp of the grokfn as the last embedding update time.
	fi, err := os.Stat(grokfn)
	Ck(err)
	lastUpdate := fi.ModTime()
	for _, doc := range g.Documents {
		// check if the document has changed.
		fi, err := os.Stat(doc.Path)
		if os.IsNotExist(err) {
			// document has been removed; remove it from the database.
			g.RemoveDocument(doc)
			update = true
			continue
		}
		Ck(err)
		if fi.ModTime().After(lastUpdate) {
			// update the embeddings.
			Debug("updating embeddings for %s ...", doc.Path)
			updated, err := g.UpdateDocument(doc)
			Ck(err)
			Debug("done\n")
			update = update || updated
		}
	}
	// garbage collect any chunks that are no longer referenced.
	g.GC()
	return
}

// AddDocument adds a document to the Grokker database. It creates the
// embeddings for the document and adds them to the database.
func (g *Grokker) AddDocument(path string) (err error) {
	defer Return(&err)
	doc := &Document{
		Path: path,
	}
	// find out if the document is already in the database.
	found := false
	for _, d := range g.Documents {
		if d.Path == doc.Path {
			found = true
			break
		}
	}
	if !found {
		// add the document to the database.
		g.Documents = append(g.Documents, doc)
	}
	// update the embeddings for the document.
	_, err = g.UpdateDocument(doc)
	Ck(err)
	return
}

// RemoveDocument removes a document from the Grokker database.
func (g *Grokker) RemoveDocument(doc *Document) (err error) {
	defer Return(&err)
	// remove the document from the database.
	for i, d := range g.Documents {
		if d.Path == doc.Path {
			g.Documents = append(g.Documents[:i], g.Documents[i+1:]...)
			break
		}
	}
	// the document chunks are still in the database, but they will be
	// removed during garbage collection.
	return
}

// GC removes any chunks that are no longer referenced by any document.
func (g *Grokker) GC() (err error) {
	defer Return(&err)
	// for each chunk, check if it is referenced by any document.
	// if not, remove it from the database.
	oldLen := len(g.Chunks)
	newChunks := make([]*Chunk, 0, len(g.Chunks))
	for _, chunk := range g.Chunks {
		// check if the chunk is referenced by any document.
		referenced := false
		for _, doc := range g.Documents {
			if doc.Path == chunk.Document.Path {
				referenced = true
				break
			}
		}
		if referenced {
			newChunks = append(newChunks, chunk)
		}
	}
	g.Chunks = newChunks
	newLen := len(g.Chunks)
	Debug("garbage collected %d chunks from the database", oldLen-newLen)
	return
}

// UpdateDocument updates the embeddings for a document and returns
// true if the document was updated.
func (g *Grokker) UpdateDocument(doc *Document) (updated bool, err error) {
	// XXX much of this code is inefficient and will be replaced
	// when we have a kv store.
	defer Return(&err)
	Debug("updating embeddings for %s ...", doc.Path)
	// break the doc up into chunks.
	chunkStrings, err := g.chunkStrings(doc)
	Ck(err)
	// get a list of the existing chunks for this document.
	var oldChunks []*Chunk
	var newChunkStrings []string
	for _, chunk := range g.Chunks {
		if chunk.Document.Path == doc.Path {
			oldChunks = append(oldChunks, chunk)
		}
	}
	Debug("found %d existing chunks", len(oldChunks))
	// for each chunk, check if it already exists in the database.
	for _, chunkString := range chunkStrings {
		found := false
		for _, oldChunk := range oldChunks {
			if oldChunk.Text == chunkString {
				// the chunk already exists in the database.  remove it from the list of old chunks.
				found = true
				for i, c := range oldChunks {
					if c == oldChunk {
						oldChunks = append(oldChunks[:i], oldChunks[i+1:]...)
						break
					}
				}
				break
			}
		}
		if !found {
			// the chunk does not exist in the database.  add it.
			updated = true
			newChunkStrings = append(newChunkStrings, chunkString)
		}
	}
	Debug("found %d new chunks", len(newChunkStrings))
	// orphaned chunks will be garbage collected.

	// For each text chunk, generate an embedding using the
	// openai.Embedding.create() function. Store the embeddings for each
	// chunk in a data structure such as a list or dictionary.
	embeddings, err := g.CreateEmbeddings(newChunkStrings)
	Ck(err)
	for i, text := range newChunkStrings {
		chunk := &Chunk{
			Document:  doc,
			Text:      text,
			Embedding: embeddings[i],
		}
		g.Chunks = append(g.Chunks, chunk)
	}
	return
}

// Embeddings returns the embeddings for a slice of text chunks.
func (g *Grokker) CreateEmbeddings(texts []string) (embeddings [][]float64, err error) {
	// use github.com/fabiustech/openai library
	c := g.embeddingClient
	// only send 100 chunks at a time
	for i := 0; i < len(texts); i += 100 {
		end := i + 100
		if end > len(texts) {
			end = len(texts)
		}
		req := &openai.EmbeddingRequest{
			Input: texts[i:end],
			Model: models.AdaEmbeddingV2,
		}
		res, err := c.CreateEmbeddings(context.Background(), req)
		Ck(err)
		for _, em := range res.Data {
			embeddings = append(embeddings, em.Embedding)
		}
	}
	Debug("created %d embeddings", len(embeddings))
	return
}

// chunks returns a slice containing the chunk strings for a document.
func (g *Grokker) chunkStrings(doc *Document) (c []string, err error) {
	defer Return(&err)
	// Break up the document into smaller text chunks or passages,
	// each with a length of up to 8192 tokens (the maximum input size for
	// the text-embedding-ada-002 model used by the Embeddings API).
	txt, err := ioutil.ReadFile(doc.Path)
	Ck(err)
	// for now, just split on paragraph boundaries
	paragraphs := strings.Split(string(txt), "\n\n")
	for _, paragraph := range paragraphs {
		// split the paragraph into chunks if it's too long.
		// XXX replace with a real tokenizer.
		for len(paragraph) > 0 {
			if len(paragraph) > g.MaxChunkSize {
				c = append(c, paragraph[:g.MaxChunkSize])
				paragraph = paragraph[g.MaxChunkSize:]
			} else {
				c = append(c, paragraph)
				paragraph = ""
			}
		}
	}
	return
}

// (4) When a question is asked, use the embeddings of the document
// chunks to find the most relevant passages for the question. You can
// use a similarity measure such as cosine similarity to compare the
// embeddings of the question and each document chunk, and return the
// chunks with the highest similarity scores.

// FindChunks returns the K most relevant chunks for a query.
func (g *Grokker) FindChunks(query string, K int) (chunks []*Chunk, err error) {
	defer Return(&err)
	// get the embeddings for the query.
	embeddings, err := g.CreateEmbeddings([]string{query})
	Ck(err)
	queryEmbedding := embeddings[0]
	// find the most similar chunks.
	chunks = g.SimilarChunks(queryEmbedding, K)
	return
}

// SimilarChunks returns the K most similar chunks to an embedding.
// If K is 0, it returns all chunks.
func (g *Grokker) SimilarChunks(embedding []float64, K int) (chunks []*Chunk) {
	Debug("chunks in database: %d", len(g.Chunks))
	// find the most similar chunks.
	type Sim struct {
		chunk *Chunk
		score float64
	}
	sims := make([]Sim, 0, len(g.Chunks))
	for _, chunk := range g.Chunks {
		score := Similarity(embedding, chunk.Embedding)
		sims = append(sims, Sim{chunk, score})
	}
	// sort the chunks by similarity.
	sort.Slice(sims, func(i, j int) bool {
		return sims[i].score > sims[j].score
	})
	// return the top K chunks.
	if K == 0 {
		K = len(sims)
	}
	for i := 0; i < K && i < len(sims); i++ {
		chunks = append(chunks, sims[i].chunk)
	}
	Debug("found %d similar chunks", len(chunks))
	return
}

// Similarity returns the cosine similarity between two embeddings.
func Similarity(a, b []float64) float64 {
	var dot, magA, magB float64
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}

// (5) Provide the most relevant document chunks to the
// openai.Completion.create() function as additional context for
// generating a response. This will allow the model to better
// understand the context of the question and generate a more relevant
// response.

// Answer returns the answer to a question.
func (g *Grokker) Answer(question string, global bool) (resp oai.ChatCompletionResponse, query string, err error) {
	defer Return(&err)
	// get all chunks, sorted by similarity to the question.
	chunks, err := g.FindChunks(question, 0)
	Ck(err)
	// reserve 50% of the chunks for the response.
	maxSize := int(float64(g.MaxChunkSize) * 0.5)
	// use chunks as context for the answer until we reach the max size.
	var context string
	for _, chunk := range chunks {
		context += chunk.Text + "\n\n"
		if len(context)+len(promptTmpl) > maxSize {
			break
		}
	}
	Debug("using %d chunks as context", len(chunks))

	// generate the answer.
	resp, query, err = g.Generate(question, context, global)
	return
}

// Use the openai.Completion.create() function to generate a
// response to the question. You can use the prompt parameter to
// provide the question, and the max_tokens parameter to limit the
// length of the response.

// var promptTmpl = `You are a helpful assistant.  Answer the following question and summarize the context:
// var promptTmpl = `You are a helpful assistant.
var promptTmpl = `{{.Question}}

Context:
{{.Context}}`

var XXXpromptTmpl = `{{.Question}}`

// Generate returns the answer to a question.
func (g *Grokker) Generate(question, ctxt string, global bool) (resp oai.ChatCompletionResponse, query string, err error) {
	defer Return(&err)

	/*
		var systemText string
		if global {
			systemText = "You are a helpful assistant that provides answers from everything you know, as well as from the context provided in this chat."
		} else {
			systemText = "You are a helpful assistant that provides answers from the context provided in this chat."
		}
	*/

	// XXX don't exceed max tokens
	messages := []oai.ChatCompletionMessage{
		{
			Role:    oai.ChatMessageRoleSystem,
			Content: "You are a helpful assistant.",
		},
	}

	// first get global knowledge
	if global {
		messages = append(messages, oai.ChatCompletionMessage{
			Role:    oai.ChatMessageRoleUser,
			Content: question,
		})
		resp, err = g.chat(messages)
		Ck(err)
		// add the response to the messages.
		messages = append(messages, oai.ChatCompletionMessage{
			Role:    oai.ChatMessageRoleAssistant,
			Content: resp.Choices[0].Message.Content,
		})
	}

	// add context from local sources
	messages = append(messages, []oai.ChatCompletionMessage{
		{
			Role:    oai.ChatMessageRoleUser,
			Content: ctxt,
		},
		{
			Role:    oai.ChatMessageRoleAssistant,
			Content: "Great! I've read the context.",
		},
	}...)

	// now ask the question
	messages = append(messages, oai.ChatCompletionMessage{
		Role:    oai.ChatMessageRoleUser,
		Content: question,
	})

	// get the answer
	resp, err = g.chat(messages)

	// fmt.Println(resp.Choices[0].Message.Content)
	// Pprint(messages)
	// Pprint(resp)
	return
}

// chat uses the openai API to continue a conversation given a
// (possibly synthesized) message history.
func (g *Grokker) chat(messages []oai.ChatCompletionMessage) (resp oai.ChatCompletionResponse, err error) {
	defer Return(&err)

	Debug("chat: messages: %v", messages)

	// use 	"github.com/sashabaranov/go-openai"
	client := g.chatClient
	resp, err = client.CreateChatCompletion(
		context.Background(),
		oai.ChatCompletionRequest{
			Model:    oai.GPT3Dot5Turbo,
			Messages: messages,
		},
	)
	Ck(err, "%#v", messages)
	totalBytes := 0
	for _, msg := range messages {
		totalBytes += len(msg.Content)
	}
	totalBytes += len(resp.Choices[0].Message.Content)
	ratio := float64(totalBytes) / float64(resp.Usage.TotalTokens)
	Debug("total tokens: %d  char/token ratio: %.1f\n", resp.Usage.TotalTokens, ratio)
	return
}

// RefreshEmbeddings refreshes the embeddings for all documents in the
// database.
func (g *Grokker) RefreshEmbeddings() (err error) {
	defer Return(&err)
	// regenerate the embeddings for each document.
	for _, doc := range g.Documents {
		_, err = g.UpdateDocument(doc)
		Ck(err)
	}
	g.GC()
	return
}

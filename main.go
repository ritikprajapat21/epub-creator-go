package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/go-shiori/go-epub"
	"golang.org/x/net/html"
)

const fetchURL = "https://www.gutenberg.org/cache/epub/1184/pg1184-images.html"
const outputEPUB = "output.epub"
const tempImageDir = "temp_images"
const outputHTML = "output.html"

func main() {
	// Fetch or load the HTML content
	body, baseURL, err := fetchOrLoadHTML(fetchURL, outputHTML)
	if err != nil {
		log.Fatalf("Error fetching or loading HTML: %v", err)
		os.Exit(1)
	}

	// Parse the HTML
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		log.Fatalf("Error parsing HTML: %v", err)
	}

	// Create EPUB
	e, err := epub.NewEpub("Count of Monte Cristo")
	if err != nil {
		log.Fatalf("Error creating EPUB: %v", err)
		os.Exit(1)
	}
	e.SetAuthor("ritikprajapat21") // You can change this

	// Create temporary directory for images
	if err := os.MkdirAll(tempImageDir, 0755); err != nil {
		log.Fatalf("Error creating temp image directory: %v", err)
	}
	// defer os.RemoveAll(tempImageDir) // Clean up temp directory

	// Extract content and images
	var currentSection strings.Builder
	var sectionTitle string = "Chapter 1" // Default title

	var extractText func(*html.Node)
	extractText = func(n *html.Node) {
		if n.Type == html.ElementNode {
			// Basic section handling (can be improved based on actual HTML structure)
			if n.Data == "h3" {
				if currentSection.Len() > 0 {
					// Add previous section to EPUB
					_, err := e.AddSection(currentSection.String(), sectionTitle, "", "")
					if err != nil {
						log.Printf("Warning: Could not add section '%s': %v", sectionTitle, err)
					}
					currentSection.Reset() // Start new section
				}
				sectionTitle = getText(n) // Get title from heading
				if sectionTitle == "" {
					sectionTitle = "Unnamed Section"
				}
			}

			// Handle images
			if n.Data == "img" {
				for _, attr := range n.Attr {
					if attr.Key == "src" {
						imgURL := attr.Val
						// Resolve relative URLs
						absoluteImgURL, err := baseURL.Parse(imgURL)
						if err != nil {
							log.Printf("Warning: Could not parse image URL '%s': %v", imgURL, err)
							continue
						}

						// Download or load image
						imgPath, err := fetchOrLoadImage(absoluteImgURL.String(), tempImageDir)
						if err != nil {
							log.Printf("Warning: Could not download or load image '%s': %v", absoluteImgURL.String(), err)
							continue
						}

						// Add image to EPUB and get internal path
						epubImgPath, err := e.AddImage(imgPath, "")
						if err != nil {
							log.Printf("Warning: Could not add image '%s' to EPUB: %v", imgPath, err)
							// Don't remove the local file yet if adding failed
							continue
						}

						// Append img tag to current section content
						currentSection.WriteString(fmt.Sprintf(`<p><img src="%s" alt="Image"/></p>`, epubImgPath))
						// No need to remove imgPath here, defer os.RemoveAll(tempImageDir) handles cleanup
						break // Found src, move to next node
					}
				}
			}
		} else if n.Type == html.TextNode {
			// Append text content, trimming whitespace
			trimmedData := strings.TrimSpace(n.Data)
			if trimmedData != "" {
				// Basic paragraph wrapping
				if !strings.HasSuffix(currentSection.String(), "</p>") && currentSection.Len() > 0 {
					// If the last thing wasn't a closing p tag, start a new one.
					// This is a simplification; real HTML structure might need more complex handling.
					currentSection.WriteString("<p>")
				} else if currentSection.Len() == 0 {
					// currentSection.WriteString("<p>")
				}
				currentSection.WriteString("<p>" + html.EscapeString(trimmedData) + " ") // Add space between text nodes
				// Add closing tag tentatively; might be overwritten by next element or text
				if !strings.HasSuffix(currentSection.String(), "</p>") {
					currentSection.WriteString("</p>")
				}
			}
		}

		// Recursively process child nodes
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractText(c)
		}
	}

	// Find the body node to start extraction
	var bodyNode *html.Node
	var findBody func(*html.Node)
	findBody = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "body" {
			bodyNode = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findBody(c)
			if bodyNode != nil {
				return
			}
		}
	}
	findBody(doc)

	if bodyNode != nil {
		extractText(bodyNode)
	} else {
		log.Println("Warning: Could not find body node in HTML, extracting from root.")
		extractText(doc) // Fallback to extracting from root if body not found
	}

	// Add the last section if it has content
	if currentSection.Len() > 0 {
		_, err := e.AddSection(currentSection.String(), sectionTitle, "", "")
		if err != nil {
			log.Printf("Warning: Could not add final section '%s': %v", sectionTitle, err)
		}
	}

	// Write EPUB file
	err = e.Write(outputEPUB)
	if err != nil {
		log.Fatalf("Error writing EPUB file: %v", err)
	}

	fmt.Printf("Successfully created EPUB: %s\n", outputEPUB)
}

// fetchOrLoadHTML fetches the HTML content from a given URL if the local file doesn't exist
// or loads it from the local file. It returns the body content as bytes and the base URL.
func fetchOrLoadHTML(urlStr, filePath string) ([]byte, *url.URL, error) {
	content, err := os.ReadFile(filePath)
	if err == nil {
		baseURL, err := url.Parse(urlStr)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse base URL: %w", err)
		}
		return content, baseURL, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("failed to read local HTML file '%s': %w", filePath, err)
	}

	// File doesn't exist, fetch from URL
	resp, err := http.Get(urlStr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get URL '%s': %w", urlStr, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("bad status for URL '%s': %s", urlStr, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body from '%s': %w", urlStr, err)
	}

	// Save the fetched content to the local file
	err = os.WriteFile(filePath, body, 0644)
	if err != nil {
		log.Printf("Warning: Failed to save HTML to '%s': %v", filePath, err)
	}

	baseURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse base URL '%s': %w", urlStr, err)
	}

	return body, baseURL, nil
}

// fetchOrLoadImage downloads an image from a URL and saves it to a temporary directory if it doesn't exist locally.
// It returns the path to the (newly downloaded or existing) image file.
func fetchOrLoadImage(imgURL string, dir string) (string, error) {
	parsedURL, err := url.Parse(imgURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse image URL '%s': %w", imgURL, err)
	}
	filename := path.Base(parsedURL.Path)
	if filename == "." || filename == "/" { // Handle cases where path is minimal
		filename = "image_" + strings.ReplaceAll(parsedURL.Host, ".", "_") + ".tmp" // Create a fallback name
	}
	// Ensure filename is safe (basic sanitization)
	safeFilename := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, filename)

	filepath := path.Join(dir, safeFilename)

	// Check if the image already exists
	if _, err := os.Stat(filepath); err == nil {
		return filepath, nil // Image exists, return the path
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("failed to check if image exists at '%s': %w", filepath, err)
	}

	// Image doesn't exist, download it
	resp, err := http.Get(imgURL)
	if err != nil {
		return "", fmt.Errorf("failed to get image URL '%s': %w", imgURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status for image '%s': %s", imgURL, resp.Status)
	}

	// Create the directory if it doesn't exist (should already be created in main, but just in case)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory '%s': %w", dir, err)
	}

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to create image file '%s': %w", filepath, err)
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to save image to '%s': %w", filepath, err)
	}

	return filepath, nil
}

// getText extracts and concatenates all text nodes within a given node.
func getText(n *html.Node) string {
	var b strings.Builder
	var extract func(*html.Node)
	extract = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(strings.TrimSpace(node.Data))
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}
	extract(n)
	return b.String()
}

// Helper function to read file content (replaces os.ReadFile for clarity in example)
// Note: This function is not used in the final version but kept for reference
// if you were reading from a local file initially.
func readFileContent(filename string) ([]byte, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("error reading file %s: %w", filename, err)
	}
	return content, nil
}

// getAndSave fetches HTML and saves it to a local file.
// Note: This function is replaced by fetchOrLoadHTML in the final version.
// Kept for reference from the original code.
func getAndSave() (*os.File, error) {
	resp, err := http.Get("https://www.gutenberg.org/cache/epub/1184/pg1184-images.html#linkC2HCH0002") // Original URL had fragment
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Using os.Create simplifies file opening for writing, truncating if exists.
	f, err := os.Create("example.html")
	if err != nil {
		// Return nil for file pointer on error
		return nil, err
	}
	// No need to return f if we close it here. Let's close it immediately after write.
	// defer f.Close() // Defer is useful if more operations follow, but here we write and close.

	_, err = f.Write(body)
	if err != nil {
		f.Close() // Close file before returning error
		return nil, err
	}

	err = f.Close() // Close the file explicitly after successful write
	if err != nil {
		// Log or return this error as well if closing fails
		return nil, err
	}

	// Since the file is closed, we can't return the *os.File handle in a usable state.
	// The function signature might need adjustment based on how it's used.
	// Returning nil, nil might be appropriate if the goal is just to save the file.
	return nil, nil // Adjusted return based on closing the file
}

// Helper function needed for html.Parse
type bytesReader struct {
	*bytes.Reader
}

// package main
//
// import (
// 	"bytes"
// 	"errors"
// 	"fmt"
// 	"io"
// 	"log"
// 	"net/http"
// 	"net/url"
// 	"os"
// 	"path"
// 	"strings"
//
// 	"github.com/go-shiori/go-epub"
// 	"golang.org/x/net/html"
// )
//
// const fetchURL = "https://www.gutenberg.org/cache/epub/1184/pg1184-images.html"
// const outputEPUB = "output.epub"
// const tempImageDir = "temp_images"
// const outputHTML = "output.html"
//
// func main() {
// 	// Fetch the HTML content
// 	body, baseURL, err := fetchHTMLAndSave(fetchURL)
// 	if err != nil {
// 		log.Fatalf("Error fetching HTML: %v", err)
// 		os.Exit(1)
// 	}
//
// 	// Parse the HTML
// 	doc, err := html.Parse(bytes.NewReader(body))
// 	if err != nil {
// 		log.Fatalf("Error parsing HTML: %v", err)
// 	}
//
// 	// Create EPUB
// 	e, err := epub.NewEpub("Fetched EPUB")
// 	if err != nil {
// 		log.Fatalf("Error creating EPUB: %v", err)
// 		os.Exit(1)
// 	}
// 	e.SetAuthor("Cline") // You can change this
//
// 	// Create temporary directory for images
// 	if err := os.MkdirAll(tempImageDir, 0755); err != nil {
// 		log.Fatalf("Error creating temp image directory: %v", err)
// 	}
// 	defer os.RemoveAll(tempImageDir) // Clean up temp directory
//
// 	// Extract content and images
// 	var currentSection strings.Builder
// 	var sectionTitle string = "Chapter 1" // Default title
//
// 	var extractText func(*html.Node)
// 	extractText = func(n *html.Node) {
// 		if n.Type == html.ElementNode {
// 			// Basic section handling (can be improved based on actual HTML structure)
// 			if n.Data == "h1" || n.Data == "h2" || n.Data == "h3" {
// 				if currentSection.Len() > 0 {
// 					// Add previous section to EPUB
// 					_, err := e.AddSection(currentSection.String(), sectionTitle, "", "")
// 					if err != nil {
// 						log.Printf("Warning: Could not add section '%s': %v", sectionTitle, err)
// 					}
// 					currentSection.Reset() // Start new section
// 				}
// 				sectionTitle = getText(n) // Get title from heading
// 				if sectionTitle == "" {
// 					sectionTitle = "Unnamed Section"
// 				}
// 			}
//
// 			// Handle images
// 			if n.Data == "img" {
// 				for _, attr := range n.Attr {
// 					if attr.Key == "src" {
// 						imgURL := attr.Val
// 						// Resolve relative URLs
// 						absoluteImgURL, err := baseURL.Parse(imgURL)
// 						if err != nil {
// 							log.Printf("Warning: Could not parse image URL '%s': %v", imgURL, err)
// 							continue
// 						}
//
// 						// Download image
// 						imgPath, err := downloadImage(absoluteImgURL.String(), tempImageDir)
// 						if err != nil {
// 							log.Printf("Warning: Could not download image '%s': %v", absoluteImgURL.String(), err)
// 							continue
// 						}
//
// 						// Add image to EPUB and get internal path
// 						epubImgPath, err := e.AddImage(imgPath, "")
// 						if err != nil {
// 							log.Printf("Warning: Could not add image '%s' to EPUB: %v", imgPath, err)
// 							// Don't remove the local file yet if adding failed
// 							continue
// 						}
//
// 						// Append img tag to current section content
// 						currentSection.WriteString(fmt.Sprintf(`<p><img src="%s" alt="Image"/></p>`, epubImgPath))
// 						// No need to remove imgPath here, defer os.RemoveAll(tempImageDir) handles cleanup
// 						break // Found src, move to next node
// 					}
// 				}
// 			}
// 		} else if n.Type == html.TextNode {
// 			// Append text content, trimming whitespace
// 			trimmedData := strings.TrimSpace(n.Data)
// 			if trimmedData != "" {
// 				// Basic paragraph wrapping
// 				if !strings.HasSuffix(currentSection.String(), "</p>") && currentSection.Len() > 0 {
// 					// If the last thing wasn't a closing p tag, start a new one.
// 					// This is a simplification; real HTML structure might need more complex handling.
// 					currentSection.WriteString("<p>")
// 				} else if currentSection.Len() == 0 {
// 					currentSection.WriteString("<p>")
// 				}
// 				currentSection.WriteString(html.EscapeString(trimmedData) + " ") // Add space between text nodes
// 				// Add closing tag tentatively; might be overwritten by next element or text
// 				if !strings.HasSuffix(currentSection.String(), "</p>") {
// 					currentSection.WriteString("</p>")
// 				}
// 			}
// 		}
//
// 		// Recursively process child nodes
// 		for c := n.FirstChild; c != nil; c = c.NextSibling {
// 			extractText(c)
// 		}
// 	}
//
// 	// Find the body node to start extraction
// 	var bodyNode *html.Node
// 	var findBody func(*html.Node)
// 	findBody = func(n *html.Node) {
// 		if n.Type == html.ElementNode && n.Data == "body" {
// 			bodyNode = n
// 			return
// 		}
// 		for c := n.FirstChild; c != nil; c = c.NextSibling {
// 			findBody(c)
// 			if bodyNode != nil {
// 				return
// 			}
// 		}
// 	}
// 	findBody(doc)
//
// 	if bodyNode != nil {
// 		extractText(bodyNode)
// 	} else {
// 		log.Println("Warning: Could not find body node in HTML, extracting from root.")
// 		extractText(doc) // Fallback to extracting from root if body not found
// 	}
//
// 	// Add the last section if it has content
// 	if currentSection.Len() > 0 {
// 		_, err := e.AddSection(currentSection.String(), sectionTitle, "", "")
// 		if err != nil {
// 			log.Printf("Warning: Could not add final section '%s': %v", sectionTitle, err)
// 		}
// 	}
//
// 	// Write EPUB file
// 	err = e.Write(outputEPUB)
// 	if err != nil {
// 		log.Fatalf("Error writing EPUB file: %v", err)
// 	}
//
// 	fmt.Printf("Successfully created EPUB: %s\n", outputEPUB)
// }
//
// // fetchHTML fetches the HTML content from a given URL.
// // It returns the body content as bytes and the base URL for resolving relative links.
// func fetchHTMLAndSave(urlStr string) ([]byte, *url.URL, error) {
// 	r, err := os.Open(outputHTML)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to open file: %w", err)
// 	}
// 	body, err := io.ReadAll(r)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to read file: %w", err)
// 	}
//
// 	if errors.Is(err, os.ErrNotExist) {
// 		resp, err := http.Get(urlStr)
// 		if err != nil {
// 			return nil, nil, fmt.Errorf("failed to get URL: %w", err)
// 		}
// 		defer resp.Body.Close()
//
// 		if resp.StatusCode != http.StatusOK {
// 			return nil, nil, fmt.Errorf("bad status: %s", resp.Status)
// 		}
//
// 		body, err := io.ReadAll(resp.Body)
// 		if err != nil {
// 			return nil, nil, fmt.Errorf("failed to read response body: %w", err)
// 		}
//
// 		f, err := os.OpenFile(outputHTML, os.O_CREATE|os.O_RDONLY, os.ModeAppend)
// 		if err != nil {
// 			return nil, nil, fmt.Errorf("failed to open file: %w", err)
// 		}
// 		f.Write(body)
// 	}
//
// 	baseURL, err := url.Parse(urlStr)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to parse base URL: %w", err)
// 	}
//
//
// 	return body, baseURL, nil
// }
//
// // downloadImage downloads an image from a URL and saves it to a temporary directory.
// // It returns the path to the downloaded image file.
// func downloadImage(imgURL string, dir string) (string, error) {
// 	resp, err := http.Get(imgURL)
// 	if err != nil {
// 		return "", fmt.Errorf("failed to get image URL %s: %w", imgURL, err)
// 	}
// 	defer resp.Body.Close()
//
// 	if resp.StatusCode != http.StatusOK {
// 		return "", fmt.Errorf("bad status for image %s: %s", imgURL, resp.Status)
// 	}
//
// 	// Create a unique filename based on the URL path
// 	parsedURL, err := url.Parse(imgURL)
// 	if err != nil {
// 		return "", fmt.Errorf("failed to parse image URL %s: %w", imgURL, err)
// 	}
// 	filename := path.Base(parsedURL.Path)
// 	if filename == "." || filename == "/" { // Handle cases where path is minimal
// 		filename = "image_" + strings.ReplaceAll(parsedURL.Host, ".", "_") + ".tmp" // Create a fallback name
// 	}
// 	// Ensure filename is safe (basic sanitization)
// 	filename = strings.Map(func(r rune) rune {
// 		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
// 			return '_'
// 		}
// 		return r
// 	}, filename)
//
// 	filepath := path.Join(dir, filename)
//
// 	// Create the file
// 	out, err := os.Create(filepath)
// 	if err != nil {
// 		return "", fmt.Errorf("failed to create image file %s: %w", filepath, err)
// 	}
// 	defer out.Close()
//
// 	// Write the body to file
// 	_, err = io.Copy(out, resp.Body)
// 	if err != nil {
// 		return "", fmt.Errorf("failed to save image %s: %w", filepath, err)
// 	}
//
// 	return filepath, nil
// }
//
// // getText extracts and concatenates all text nodes within a given node.
// func getText(n *html.Node) string {
// 	var b strings.Builder
// 	var extract func(*html.Node)
// 	extract = func(node *html.Node) {
// 		if node.Type == html.TextNode {
// 			b.WriteString(strings.TrimSpace(node.Data))
// 		}
// 		for c := node.FirstChild; c != nil; c = c.NextSibling {
// 			extract(c)
// 		}
// 	}
// 	extract(n)
// 	return b.String()
// }
//
// // Helper function to read file content (replaces os.ReadFile for clarity in example)
// // Note: This function is not used in the final version but kept for reference
// // if you were reading from a local file initially.
// func readFileContent(filename string) ([]byte, error) {
// 	content, err := os.ReadFile(filename)
// 	if err != nil {
// 		return nil, fmt.Errorf("error reading file %s: %w", filename, err)
// 	}
// 	return content, nil
// }
//
// // getAndSave fetches HTML and saves it to a local file.
// // Note: This function is replaced by fetchHTML in the final version.
// // Kept for reference from the original code.
// func getAndSave() (*os.File, error) {
// 	resp, err := http.Get("https://www.gutenberg.org/cache/epub/1184/pg1184-images.html#linkC2HCH0002") // Original URL had fragment
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer resp.Body.Close()
//
// 	body, err := io.ReadAll(resp.Body)
// 	if err != nil {
// 		return nil, err
// 	}
//
// 	// Using os.Create simplifies file opening for writing, truncating if exists.
// 	f, err := os.Create("example.html")
// 	if err != nil {
// 		// Return nil for file pointer on error
// 		return nil, err
// 	}
// 	// No need to return f if we close it here. Let's close it immediately after write.
// 	// defer f.Close() // Defer is useful if more operations follow, but here we write and close.
//
// 	_, err = f.Write(body)
// 	if err != nil {
// 		f.Close() // Close file before returning error
// 		return nil, err
// 	}
//
// 	err = f.Close() // Close the file explicitly after successful write
// 	if err != nil {
// 		// Log or return this error as well if closing fails
// 		return nil, err
// 	}
//
// 	// Since the file is closed, we can't return the *os.File handle in a usable state.
// 	// The function signature might need adjustment based on how it's used.
// 	// Returning nil, nil might be appropriate if the goal is just to save the file.
// 	return nil, nil // Adjusted return based on closing the file
// }
//
// // Helper function needed for html.Parse
// type bytesReader struct {
// 	*bytes.Reader
// }

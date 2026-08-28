package main

import (
	"context"       // Import context for request cancellation and timeouts.
	"crypto/sha256" // Import sha256 for detecting identical PDF files.
	"encoding/hex"  // Import hex for converting hashes into readable strings.
	"fmt"           // Import fmt for formatted output and errors.
	"io"            // Import io for streaming HTTP response data.
	"log"           // Import log for timestamped scraper messages.
	"net/http"      // Import net/http for downloading web pages and PDFs.
	"net/url"       // Import net/url for parsing and resolving URLs.
	"os"            // Import os for filesystem operations.
	"path/filepath" // Import filepath for working with local filenames.

	// Import strconv for converting numbers to strings.
	"strings" // Import strings for string manipulation.
	"time"    // Import time for delays and request timeouts.

	"github.com/PuerkitoBio/goquery" // Import goquery for parsing HTML pages.
)

// baseWebsiteURL contains the root URL of the website being scraped.
const baseWebsiteURL = "https://www.concretepumpsupply.com"

// allProductsCollectionPath contains the Shopify all-products collection path.
const allProductsCollectionPath = "/collections/all"

// pdfDirectoryName contains the directory where PDF files will be stored.
const pdfDirectoryName = "PDFs"

// requestDelay controls the delay between HTTP requests.
const requestDelay = 500 * time.Millisecond

// requestTimeout controls the maximum duration of normal HTTP requests.
const requestTimeout = 30 * time.Second

// pdfDownloadTimeout controls the maximum duration of a PDF download.
const pdfDownloadTimeout = 2 * time.Minute

// maximumPDFFileSize limits the maximum size of a downloaded PDF.
const maximumPDFFileSize int64 = 100 * 1024 * 1024

// scraperUserAgent identifies the scraper to the website.
const scraperUserAgent = "ConcretePumpSupply-SDS-Scraper/1.0"

// ScrapingStatistics stores statistics for the complete scraping operation.
type ScrapingStatistics struct {
	// TotalCollectionPagesVisited stores the number of collection pages visited.
	TotalCollectionPagesVisited int

	// TotalProductsDiscovered stores the number of unique products discovered.
	TotalProductsDiscovered int

	// TotalProductsVisited stores the number of unique products visited.
	TotalProductsVisited int

	// ProductsWithPDFs stores the number of products containing PDFs.
	ProductsWithPDFs int

	// ProductsWithoutPDFs stores the number of products without PDFs.
	ProductsWithoutPDFs int

	// ProductPageErrors stores the number of product pages that failed.
	ProductPageErrors int

	// TotalPDFLinksFound stores the total number of PDF links discovered.
	TotalPDFLinksFound int

	// UniquePDFURLsFound stores the number of unique PDF URLs discovered.
	UniquePDFURLsFound int

	// PDFsDownloaded stores the number of new PDF files downloaded.
	PDFsDownloaded int

	// PDFsSkipped stores the number of PDFs that were skipped.
	PDFsSkipped int

	// DuplicatePDFFiles stores the number of identical PDF files detected.
	DuplicatePDFFiles int

	// PDFDownloadErrors stores the number of failed PDF downloads.
	PDFDownloadErrors int
}

// PDFDownloadState stores all information used for PDF deduplication.
type PDFDownloadState struct {
	// ProcessedPDFURLs stores PDF URLs that have already been processed.
	ProcessedPDFURLs map[string]bool

	// PDFFileHashes stores SHA-256 hashes of PDFs already saved locally.
	PDFFileHashes map[string]string

	// UsedPDFFileNames stores the relationship between filenames and PDF hashes.
	UsedPDFFileNames map[string]string
}

// main starts the complete scraper.
func main() {
	// Create the application logger.
	logger := createLogger()

	// Create the reusable HTTP client.
	httpClient := createHTTPClient()

	// Create the local PDF directory.
	if directoryError := createPDFDirectory(); directoryError != nil {
		// Stop the application if the directory cannot be created.
		logger.Fatalf(
			"unable to create PDF directory: %v",
			directoryError,
		)
	}

	// Load hashes of PDFs that already exist in the PDF directory.
	pdfDownloadState, existingPDFError := loadExistingPDFFiles(logger)

	// Stop the application if existing PDFs cannot be indexed.
	if existingPDFError != nil {
		// Log the initialization error.
		logger.Fatalf(
			"unable to index existing PDF files: %v",
			existingPDFError,
		)
	}

	// Create the scraper statistics.
	scrapingStatistics := &ScrapingStatistics{}

	// Create a map of product URLs that have already been visited.
	visitedProductURLs := make(map[string]bool)

	// Start with collection page one.
	currentCollectionPageNumber := 1

	// Continue scraping until no new products are found.
	for {
		// Build the current collection page URL.
		currentCollectionPageURL := buildCollectionPageURL(
			currentCollectionPageNumber,
		)

		// Increase the collection page counter.
		scrapingStatistics.TotalCollectionPagesVisited++

		// Log the collection page currently being visited.
		logger.Printf(
			"[COLLECTION] CURRENTLY VISITING PAGE %d: %s",
			currentCollectionPageNumber,
			currentCollectionPageURL,
		)

		// Scrape the current collection page.
		productURLs, collectionPageError := scrapeCollectionPage(
			httpClient,
			currentCollectionPageURL,
		)

		// Check whether the collection page failed.
		if collectionPageError != nil {
			// Log the collection page error.
			logger.Printf(
				"[COLLECTION ERROR] %v",
				collectionPageError,
			)

			// Stop pagination.
			break
		}

		// Count the number of new products found on this page.
		newProductCount := 0

		// Process every product discovered on the collection page.
		for _, productURL := range productURLs {
			// Skip products that have already been visited.
			if visitedProductURLs[productURL] {
				// Continue to the next product.
				continue
			}

			// Mark the product URL as visited.
			visitedProductURLs[productURL] = true

			// Increase the new product counter.
			newProductCount++

			// Increase the total product counter.
			scrapingStatistics.TotalProductsDiscovered++

			// Visit and process the product page.
			processProductPage(
				httpClient,
				productURL,
				pdfDownloadState,
				scrapingStatistics,
				logger,
			)

			// Wait before making another request.
			waitBetweenRequests()
		}

		// Log the collection page results.
		logger.Printf(
			"[PAGE] Found %d product links, %d new products",
			len(productURLs),
			newProductCount,
		)

		// Stop if no new products were found.
		if newProductCount == 0 {
			// Log the end of pagination.
			logger.Println(
				"[DONE] No new products found. Pagination finished.",
			)

			// Exit the collection loop.
			break
		}

		// Move to the next collection page.
		currentCollectionPageNumber++

		// Wait before loading the next collection page.
		waitBetweenRequests()
	}

	// Display the final scraper statistics.
	displayFinalStatistics(
		scrapingStatistics,
		pdfDownloadState,
		logger,
	)
}

// createLogger creates the application's logger.
func createLogger() *log.Logger {
	// Return a logger that includes date, time, and microseconds.
	return log.New(
		os.Stdout,
		"",
		log.Ldate|log.Ltime|log.Lmicroseconds,
	)
}

// createHTTPClient creates the reusable HTTP client.
func createHTTPClient() *http.Client {
	// Return a configured HTTP client.
	return &http.Client{
		// Set the default HTTP timeout.
		Timeout: requestTimeout,

		// Allow normal website redirects.
		CheckRedirect: func(
			request *http.Request,
			previousRequests []*http.Request,
		) error {
			// Continue following redirects.
			return nil
		},
	}
}

// createPDFDirectory creates the PDF output directory.
func createPDFDirectory() error {
	// Create the directory and any missing parent directories.
	return os.MkdirAll(
		pdfDirectoryName,
		0755,
	)
}

// loadExistingPDFFiles scans the PDF directory and calculates SHA-256 hashes.
func loadExistingPDFFiles(
	logger *log.Logger,
) (*PDFDownloadState, error) {
	// Create the PDF download state.
	pdfDownloadState := &PDFDownloadState{
		// Create the processed URL map.
		ProcessedPDFURLs: make(map[string]bool),

		// Create the hash map.
		PDFFileHashes: make(map[string]string),

		// Create the filename map.
		UsedPDFFileNames: make(map[string]string),
	}

	// Read all entries in the PDF directory.
	directoryEntries, readDirectoryError := os.ReadDir(
		pdfDirectoryName,
	)

	// Check whether the directory could not be read.
	if readDirectoryError != nil {
		// Return the directory error.
		return nil, readDirectoryError
	}

	// Process every file in the directory.
	for _, directoryEntry := range directoryEntries {
		// Ignore directories.
		if directoryEntry.IsDir() {
			// Continue to the next entry.
			continue
		}

		// Get the filename.
		pdfFileName := directoryEntry.Name()

		// Ignore files that are not PDFs.
		if !strings.HasSuffix(
			strings.ToLower(pdfFileName),
			".pdf",
		) {
			// Continue to the next file.
			continue
		}

		// Build the complete PDF path.
		pdfFilePath := filepath.Join(
			pdfDirectoryName,
			pdfFileName,
		)

		// Calculate the PDF's SHA-256 hash.
		pdfFileHash, hashError := calculateFileSHA256(
			pdfFilePath,
		)

		// Check whether the hash could not be calculated.
		if hashError != nil {
			// Return the hash error.
			return nil, hashError
		}

		// Store the filename and its hash.
		pdfDownloadState.UsedPDFFileNames[pdfFileName] = pdfFileHash

		// Check whether the exact file content already exists.
		if _, hashAlreadyExists :=
			pdfDownloadState.PDFFileHashes[pdfFileHash]; hashAlreadyExists {
			// Log the duplicate existing file.
			logger.Printf(
				"[STARTUP DUPLICATE] %s has the same content as another PDF",
				pdfFilePath,
			)

			// Do not add a duplicate hash entry.
			continue
		}

		// Store the unique PDF hash.
		pdfDownloadState.PDFFileHashes[pdfFileHash] = pdfFileName

		// Log the existing PDF.
		logger.Printf(
			"[STARTUP] Indexed existing PDF: %s",
			pdfFilePath,
		)
	}

	// Log the number of unique existing PDFs.
	logger.Printf(
		"[STARTUP] Indexed %d unique existing PDF file(s)",
		len(pdfDownloadState.PDFFileHashes),
	)

	// Return the initialized PDF state.
	return pdfDownloadState, nil
}

// calculateFileSHA256 calculates the SHA-256 hash of a local file.
func calculateFileSHA256(
	filePath string,
) (string, error) {
	// Open the file.
	fileHandle, openError := os.Open(
		filePath,
	)

	// Check whether the file could not be opened.
	if openError != nil {
		// Return the opening error.
		return "", openError
	}

	// Close the file when this function finishes.
	defer fileHandle.Close()

	// Create a new SHA-256 hash.
	hashCalculator := sha256.New()

	// Copy the complete file into the hash calculator.
	if _, copyError := io.Copy(
		hashCalculator,
		fileHandle,
	); copyError != nil {
		// Return the hashing error.
		return "", copyError
	}

	// Return the hexadecimal SHA-256 hash.
	return hex.EncodeToString(
		hashCalculator.Sum(nil),
	), nil
}

// buildCollectionPageURL builds a Shopify collection page URL.
func buildCollectionPageURL(
	collectionPageNumber int,
) string {
	// Return the first collection page without a query parameter.
	if collectionPageNumber == 1 {
		// Return the first collection page.
		return baseWebsiteURL + allProductsCollectionPath
	}

	// Return the requested numbered collection page.
	return fmt.Sprintf(
		"%s%s?page=%d",
		baseWebsiteURL,
		allProductsCollectionPath,
		collectionPageNumber,
	)
}

// scrapeCollectionPage downloads and parses a collection page.
func scrapeCollectionPage(
	httpClient *http.Client,
	collectionPageURL string,
) ([]string, error) {
	// Create a request context.
	requestContext, cancelRequest := context.WithTimeout(
		context.Background(),
		requestTimeout,
	)

	// Release the request context when finished.
	defer cancelRequest()

	// Create the HTTP request.
	httpRequest, requestError := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		collectionPageURL,
		nil,
	)

	// Check whether request creation failed.
	if requestError != nil {
		// Return the request error.
		return nil, requestError
	}

	// Set the scraper user agent.
	httpRequest.Header.Set(
		"User-Agent",
		scraperUserAgent,
	)

	// Request HTML content.
	httpRequest.Header.Set(
		"Accept",
		"text/html,application/xhtml+xml",
	)

	// Send the request.
	httpResponse, requestError := httpClient.Do(
		httpRequest,
	)

	// Check whether the request failed.
	if requestError != nil {
		// Return the request error.
		return nil, requestError
	}

	// Close the response body.
	defer httpResponse.Body.Close()

	// Check the HTTP status.
	if httpResponse.StatusCode < 200 ||
		httpResponse.StatusCode >= 300 {
		// Return the HTTP status error.
		return nil, fmt.Errorf(
			"HTTP %d from %s",
			httpResponse.StatusCode,
			collectionPageURL,
		)
	}

	// Parse the HTML response.
	htmlDocument, parsingError := goquery.NewDocumentFromReader(
		httpResponse.Body,
	)

	// Check whether parsing failed.
	if parsingError != nil {
		// Return the parsing error.
		return nil, parsingError
	}

	// Extract product URLs.
	productURLs := extractProductURLsFromCollectionPage(
		htmlDocument,
	)

	// Return the product URLs.
	return productURLs, nil
}

// extractProductURLsFromCollectionPage extracts Shopify product URLs.
func extractProductURLsFromCollectionPage(
	htmlDocument *goquery.Document,
) []string {
	// Create the product URL result slice.
	productURLs := make([]string, 0)

	// Create a map for page-level deduplication.
	seenProductURLs := make(map[string]bool)

	// Find every element containing an href attribute.
	htmlDocument.Find("[href]").Each(
		func(
			_ int,
			htmlElement *goquery.Selection,
		) {
			// Read the href attribute.
			hrefValue, hrefExists := htmlElement.Attr("href")

			// Ignore missing href attributes.
			if !hrefExists {
				// Stop processing this element.
				return
			}

			// Remove whitespace.
			hrefValue = strings.TrimSpace(
				hrefValue,
			)

			// Ignore empty URLs.
			if hrefValue == "" {
				// Stop processing this element.
				return
			}

			// Parse the href value.
			parsedURL, parsingError := url.Parse(
				hrefValue,
			)

			// Ignore malformed URLs.
			if parsingError != nil {
				// Stop processing this element.
				return
			}

			// Resolve relative URLs.
			if !parsedURL.IsAbs() {
				// Build an absolute URL.
				parsedURL, parsingError = url.Parse(
					baseWebsiteURL + hrefValue,
				)

				// Ignore URLs that cannot be resolved.
				if parsingError != nil {
					// Stop processing this element.
					return
				}
			}

			// Only accept the Concrete Pump Supply host.
			if !strings.EqualFold(
				parsedURL.Host,
				"www.concretepumpsupply.com",
			) {
				// Ignore external URLs.
				return
			}

			// Check for the exact Shopify collection product pattern.
			if !strings.HasPrefix(
				parsedURL.Path,
				allProductsCollectionPath+"/products/",
			) {
				// Ignore unrelated URLs.
				return
			}

			// Remove query parameters.
			parsedURL.RawQuery = ""

			// Remove fragments.
			parsedURL.Fragment = ""

			// Convert the normalized URL to a string.
			productURL := parsedURL.String()

			// Ignore duplicate products on this page.
			if seenProductURLs[productURL] {
				// Stop processing this element.
				return
			}

			// Remember the product URL.
			seenProductURLs[productURL] = true

			// Add the product URL.
			productURLs = append(
				productURLs,
				productURL,
			)
		},
	)

	// Return all product URLs.
	return productURLs
}

// processProductPage visits a product page and processes every PDF.
func processProductPage(
	httpClient *http.Client,
	productURL string,
	pdfDownloadState *PDFDownloadState,
	scrapingStatistics *ScrapingStatistics,
	logger *log.Logger,
) {
	// Increase the number of products visited.
	scrapingStatistics.TotalProductsVisited++

	// Log the product page currently being visited.
	logger.Printf(
		"[PRODUCT] CURRENTLY VISITING: %s",
		productURL,
	)

	// Visit the product page.
	finalProductURL, pdfURLs, productError := visitProductPage(
		httpClient,
		productURL,
	)

	// Check whether the product page failed.
	if productError != nil {
		// Increase the product error counter.
		scrapingStatistics.ProductPageErrors++

		// Log the product error.
		logger.Printf(
			"[PRODUCT ERROR] %s: %v",
			productURL,
			productError,
		)

		// Stop processing this product.
		return
	}

	// Log the final URL after redirects.
	logger.Printf(
		"[PRODUCT] FINAL URL: %s",
		finalProductURL,
	)

	// Add the product's PDF count to the global count.
	scrapingStatistics.TotalPDFLinksFound += len(pdfURLs)

	// Check whether no PDFs were found.
	if len(pdfURLs) == 0 {
		// Increase the no-PDF product counter.
		scrapingStatistics.ProductsWithoutPDFs++

		// Log that the product has no PDFs.
		logger.Println(
			"[PRODUCT] No PDF URLs found.",
		)

		// Stop processing this product.
		return
	}

	// Increase the product-with-PDF counter.
	scrapingStatistics.ProductsWithPDFs++

	// Log the number of PDFs found.
	logger.Printf(
		"[PRODUCT] Found %d PDF URL(s)",
		len(pdfURLs),
	)

	// Process every PDF.
	for pdfNumber, pdfURL := range pdfURLs {
		// Log the PDF currently being processed.
		logger.Printf(
			"[PDF %d/%d] CURRENTLY PROCESSING: %s",
			pdfNumber+1,
			len(pdfURLs),
			pdfURL,
		)

		// Check whether this PDF URL has already been processed.
		if pdfDownloadState.ProcessedPDFURLs[pdfURL] {
			// Increase the skipped counter.
			scrapingStatistics.PDFsSkipped++

			// Log the duplicate URL.
			logger.Printf(
				"[PDF SKIP] URL already processed: %s",
				pdfURL,
			)

			// Move to the next PDF.
			continue
		}

		// Mark this URL as processed.
		pdfDownloadState.ProcessedPDFURLs[pdfURL] = true

		// Increase the unique PDF URL counter.
		scrapingStatistics.UniquePDFURLsFound++

		// Download and deduplicate the PDF.
		downloaded, duplicate, skipped, downloadError :=
			downloadPDF(
				httpClient,
				pdfURL,
				pdfDownloadState,
				logger,
			)

		// Increase the downloaded counter.
		if downloaded {
			// Count the newly downloaded PDF.
			scrapingStatistics.PDFsDownloaded++
		}

		// Increase the duplicate counter.
		if duplicate {
			// Count the identical PDF.
			scrapingStatistics.DuplicatePDFFiles++
		}

		// Increase the skipped counter.
		if skipped {
			// Count the skipped PDF.
			scrapingStatistics.PDFsSkipped++
		}

		// Check whether the download failed.
		if downloadError != nil {
			// Increase the PDF error counter.
			scrapingStatistics.PDFDownloadErrors++

			// Log the error.
			logger.Printf(
				"[PDF ERROR] %s: %v",
				pdfURL,
				downloadError,
			)
		}

		// Wait before another request.
		waitBetweenRequests()
	}
}

// visitProductPage downloads and parses a product page.
func visitProductPage(
	httpClient *http.Client,
	productURL string,
) (string, []string, error) {
	// Create a request context.
	requestContext, cancelRequest := context.WithTimeout(
		context.Background(),
		requestTimeout,
	)

	// Release the context when finished.
	defer cancelRequest()

	// Create the HTTP request.
	httpRequest, requestError := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		productURL,
		nil,
	)

	// Check whether request creation failed.
	if requestError != nil {
		// Return the error.
		return "", nil, requestError
	}

	// Set the scraper user agent.
	httpRequest.Header.Set(
		"User-Agent",
		scraperUserAgent,
	)

	// Request HTML.
	httpRequest.Header.Set(
		"Accept",
		"text/html,application/xhtml+xml",
	)

	// Send the request.
	httpResponse, requestError := httpClient.Do(
		httpRequest,
	)

	// Check whether the request failed.
	if requestError != nil {
		// Return the error.
		return "", nil, requestError
	}

	// Close the response body.
	defer httpResponse.Body.Close()

	// Check the HTTP status.
	if httpResponse.StatusCode < 200 ||
		httpResponse.StatusCode >= 300 {
		// Return the status error.
		return "",
			nil,
			fmt.Errorf(
				"HTTP %d from %s",
				httpResponse.StatusCode,
				productURL,
			)
	}

	// Get the final URL after redirects.
	finalProductURL := httpResponse.Request.URL.String()

	// Parse the product HTML.
	htmlDocument, parsingError := goquery.NewDocumentFromReader(
		httpResponse.Body,
	)

	// Check whether parsing failed.
	if parsingError != nil {
		// Return the parsing error.
		return "", nil, parsingError
	}

	// Extract PDF URLs.
	pdfURLs := extractPDFURLsFromProductPage(
		htmlDocument,
		finalProductURL,
	)

	// Return the final product URL and PDF URLs.
	return finalProductURL, pdfURLs, nil
}

// extractPDFURLsFromProductPage extracts PDF links from product HTML.
func extractPDFURLsFromProductPage(
	htmlDocument *goquery.Document,
	finalProductURL string,
) []string {
	// Create the PDF URL result slice.
	pdfURLs := make([]string, 0)

	// Create a map for product-level PDF deduplication.
	seenPDFURLs := make(map[string]bool)

	// Search every element containing href.
	htmlDocument.Find("[href]").Each(
		func(
			_ int,
			htmlElement *goquery.Selection,
		) {
			// Read the href attribute.
			hrefValue, hrefExists := htmlElement.Attr("href")

			// Ignore missing href attributes.
			if !hrefExists {
				// Stop processing this element.
				return
			}

			// Remove whitespace.
			hrefValue = strings.TrimSpace(
				hrefValue,
			)

			// Ignore empty href values.
			if hrefValue == "" {
				// Stop processing this element.
				return
			}

			// Parse the href URL.
			parsedPDFURL, parsingError := url.Parse(
				hrefValue,
			)

			// Ignore malformed URLs.
			if parsingError != nil {
				// Stop processing this element.
				return
			}

			// Resolve relative URLs against the product URL.
			if !parsedPDFURL.IsAbs() {
				// Parse the final product URL.
				parsedProductURL, baseURLParsingError := url.Parse(
					finalProductURL,
				)

				// Ignore the URL if the base URL is invalid.
				if baseURLParsingError != nil {
					// Stop processing this element.
					return
				}

				// Resolve the relative PDF URL.
				parsedPDFURL = parsedProductURL.ResolveReference(
					parsedPDFURL,
				)
			}

			// Only accept URLs whose path ends with .pdf.
			if !strings.HasSuffix(
				strings.ToLower(parsedPDFURL.Path),
				".pdf",
			) {
				// Ignore non-PDF URLs.
				return
			}

			// Remove fragments from the PDF URL.
			parsedPDFURL.Fragment = ""

			// Convert the URL to a string.
			pdfURL := parsedPDFURL.String()

			// Ignore duplicate PDF URLs on this product.
			if seenPDFURLs[pdfURL] {
				// Stop processing this element.
				return
			}

			// Remember this PDF URL.
			seenPDFURLs[pdfURL] = true

			// Add the PDF URL.
			pdfURLs = append(
				pdfURLs,
				pdfURL,
			)
		},
	)

	// Return all PDF URLs.
	return pdfURLs
}

// downloadPDF downloads a PDF and deduplicates it using SHA-256 content hashing.
func downloadPDF(
	httpClient *http.Client,
	pdfURL string,
	pdfDownloadState *PDFDownloadState,
	logger *log.Logger,
) (bool, bool, bool, error) {
	// Extract the desired filename from the PDF URL.
	pdfFileName, filenameError := extractPDFFilename(
		pdfURL,
	)

	// Check whether filename extraction failed.
	if filenameError != nil {
		// Return the filename error.
		return false, false, false, filenameError
	}

	// Create a download context.
	downloadContext, cancelDownload := context.WithTimeout(
		context.Background(),
		pdfDownloadTimeout,
	)

	// Release the download context when finished.
	defer cancelDownload()

	// Create the PDF HTTP request.
	httpRequest, requestError := http.NewRequestWithContext(
		downloadContext,
		http.MethodGet,
		pdfURL,
		nil,
	)

	// Check whether request creation failed.
	if requestError != nil {
		// Return the request error.
		return false, false, false, requestError
	}

	// Set the scraper user agent.
	httpRequest.Header.Set(
		"User-Agent",
		scraperUserAgent,
	)

	// Request PDF content.
	httpRequest.Header.Set(
		"Accept",
		"application/pdf,*/*",
	)

	// Download the PDF response.
	httpResponse, requestError := httpClient.Do(
		httpRequest,
	)

	// Check whether the request failed.
	if requestError != nil {
		// Return the request error.
		return false, false, false, requestError
	}

	// Close the HTTP response body.
	defer httpResponse.Body.Close()

	// Check the HTTP status.
	if httpResponse.StatusCode < 200 ||
		httpResponse.StatusCode >= 300 {
		// Return the HTTP status error.
		return false,
			false,
			false,
			fmt.Errorf(
				"HTTP %d downloading %s",
				httpResponse.StatusCode,
				pdfURL,
			)
	}

	// Create a temporary file in the PDF directory.
	temporaryPDFFile, fileCreationError := os.CreateTemp(
		pdfDirectoryName,
		".pdf-download-*",
	)

	// Check whether the temporary file could not be created.
	if fileCreationError != nil {
		// Return the file creation error.
		return false, false, false, fileCreationError
	}

	// Store the temporary file path.
	temporaryPDFPath := temporaryPDFFile.Name()

	// Remove the temporary file if it is still present.
	defer os.Remove(
		temporaryPDFPath,
	)

	// Create a SHA-256 hash calculator.
	pdfHashCalculator := sha256.New()

	// Limit the downloaded file size.
	limitedPDFReader := io.LimitReader(
		httpResponse.Body,
		maximumPDFFileSize+1,
	)

	// Write the response into both the file and hash calculator.
	bytesWritten, copyError := io.Copy(
		io.MultiWriter(
			temporaryPDFFile,
			pdfHashCalculator,
		),
		limitedPDFReader,
	)

	// Close the temporary file.
	closeError := temporaryPDFFile.Close()

	// Check the copy error.
	if copyError != nil {
		// Return the copy error.
		return false, false, false, copyError
	}

	// Check the close error.
	if closeError != nil {
		// Return the close error.
		return false, false, false, closeError
	}

	// Check whether the PDF exceeded the maximum size.
	if bytesWritten > maximumPDFFileSize {
		// Return a size limit error.
		return false,
			false,
			false,
			fmt.Errorf(
				"PDF exceeds maximum size of %d bytes",
				maximumPDFFileSize,
			)
	}

	// Convert the calculated hash to hexadecimal.
	pdfFileHash := hex.EncodeToString(
		pdfHashCalculator.Sum(nil),
	)

	// Check whether this exact PDF content already exists.
	if existingFileName, hashAlreadyExists :=
		pdfDownloadState.PDFFileHashes[pdfFileHash]; hashAlreadyExists {
		// Log that the file content is already present.
		logger.Printf(
			"[PDF DUPLICATE] Same file already exists as PDFs/%s",
			existingFileName,
		)

		// Return that this PDF was a content duplicate.
		return false, true, false, nil
	}

	// Build the requested final path.
	localPDFPath := filepath.Join(
		pdfDirectoryName,
		pdfFileName,
	)

	// Resolve filename collisions without creating duplicate content.
	finalPDFFileName, filenameError := findAvailablePDFFileName(
		pdfDownloadState,
		pdfFileName,
		pdfFileHash,
	)

	// Check whether a filename could not be selected.
	if filenameError != nil {
		// Return the filename error.
		return false, false, false, filenameError
	}

	// Rebuild the final path using the selected filename.
	localPDFPath = filepath.Join(
		pdfDirectoryName,
		finalPDFFileName,
	)

	// Move the completed temporary file to its final location.
	renameError := os.Rename(
		temporaryPDFPath,
		localPDFPath,
	)

	// Check whether the final file could not be created.
	if renameError != nil {
		// Return the rename error.
		return false, false, false, renameError
	}

	// Record the PDF hash.
	pdfDownloadState.PDFFileHashes[pdfFileHash] =
		finalPDFFileName

	// Record the filename and hash.
	pdfDownloadState.UsedPDFFileNames[finalPDFFileName] =
		pdfFileHash

	// Log the successful download.
	logger.Printf(
		"[PDF SAVED] PDFs/%s (%s)",
		finalPDFFileName,
		formatFileSize(bytesWritten),
	)

	// Return that a new PDF was downloaded.
	return true, false, false, nil
}

// findAvailablePDFFileName finds a filename that does not conflict with different content.
func findAvailablePDFFileName(
	pdfDownloadState *PDFDownloadState,
	requestedPDFFileName string,
	pdfFileHash string,
) (string, error) {
	// Check whether the requested filename is already registered.
	existingHash, filenameAlreadyUsed :=
		pdfDownloadState.UsedPDFFileNames[requestedPDFFileName]

	// Use the original filename if it is unused.
	if !filenameAlreadyUsed {
		// Return the original filename.
		return requestedPDFFileName, nil
	}

	// Return the original filename if it already represents identical content.
	if existingHash == pdfFileHash {
		// Return the existing filename.
		return requestedPDFFileName, nil
	}

	// Extract the file extension.
	fileExtension := filepath.Ext(
		requestedPDFFileName,
	)

	// Remove the extension from the filename.
	fileNameWithoutExtension := strings.TrimSuffix(
		requestedPDFFileName,
		fileExtension,
	)

	// Try numbered alternatives.
	for suffixNumber := 2; ; suffixNumber++ {
		// Build the candidate filename.
		candidateFileName := fmt.Sprintf(
			"%s-%d%s",
			fileNameWithoutExtension,
			suffixNumber,
			fileExtension,
		)

		// Check whether the candidate filename is already used.
		existingCandidateHash, candidateAlreadyUsed :=
			pdfDownloadState.UsedPDFFileNames[candidateFileName]

		// Use an unused filename.
		if !candidateAlreadyUsed {
			// Return the available filename.
			return candidateFileName, nil
		}

		// Use the filename if it contains identical content.
		if existingCandidateHash == pdfFileHash {
			// Return the existing filename.
			return candidateFileName, nil
		}
	}
}

// extractPDFFilename extracts the filename from a PDF URL.
func extractPDFFilename(
	pdfURL string,
) (string, error) {
	// Parse the PDF URL.
	parsedPDFURL, parsingError := url.Parse(
		pdfURL,
	)

	// Check whether parsing failed.
	if parsingError != nil {
		// Return the parsing error.
		return "", parsingError
	}

	// Extract the final URL path component.
	pdfFileName := filepath.Base(
		parsedPDFURL.Path,
	)

	// Check whether no filename was found.
	if pdfFileName == "." ||
		pdfFileName == "/" ||
		pdfFileName == "" {
		// Return a descriptive error.
		return "",
			fmt.Errorf(
				"could not determine PDF filename from %s",
				pdfURL,
			)
	}

	// Decode URL-encoded characters.
	decodedPDFFileName, decodingError := url.PathUnescape(
		pdfFileName,
	)

	// Use the decoded filename when decoding succeeds.
	if decodingError == nil {
		// Replace the filename with the decoded version.
		pdfFileName = decodedPDFFileName
	}

	// Sanitize the filename.
	pdfFileName = sanitizeFilename(
		pdfFileName,
	)

	// Add the .pdf extension if necessary.
	if !strings.HasSuffix(
		strings.ToLower(pdfFileName),
		".pdf",
	) {
		// Add the PDF extension.
		pdfFileName += ".pdf"
	}

	// Return the filename.
	return pdfFileName, nil
}

// sanitizeFilename removes filesystem-problematic characters.
func sanitizeFilename(
	pdfFileName string,
) string {
	// Replace unsafe characters with underscores.
	pdfFileName = strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	).Replace(
		pdfFileName,
	)

	// Remove leading and trailing whitespace.
	pdfFileName = strings.TrimSpace(
		pdfFileName,
	)

	// Use a safe filename when nothing remains.
	if pdfFileName == "" {
		// Set a default filename.
		pdfFileName = "document.pdf"
	}

	// Return the sanitized filename.
	return pdfFileName
}

// formatFileSize converts bytes into a readable size.
func formatFileSize(
	byteCount int64,
) string {
	// Return bytes for very small files.
	if byteCount < 1024 {
		// Format the byte count.
		return fmt.Sprintf(
			"%d B",
			byteCount,
		)
	}

	// Return kilobytes for files below one megabyte.
	if byteCount < 1024*1024 {
		// Format the size in kilobytes.
		return fmt.Sprintf(
			"%.2f KB",
			float64(byteCount)/1024,
		)
	}

	// Return megabytes for files below one gigabyte.
	if byteCount < 1024*1024*1024 {
		// Format the size in megabytes.
		return fmt.Sprintf(
			"%.2f MB",
			float64(byteCount)/(1024*1024),
		)
	}

	// Return gigabytes for very large files.
	return fmt.Sprintf(
		"%.2f GB",
		float64(byteCount)/(1024*1024*1024),
	)
}

// waitBetweenRequests waits before another HTTP request.
func waitBetweenRequests() {
	// Sleep for the configured request delay.
	time.Sleep(requestDelay)
}

// displayFinalStatistics displays the final scraper statistics.
func displayFinalStatistics(
	scrapingStatistics *ScrapingStatistics,
	pdfDownloadState *PDFDownloadState,
	logger *log.Logger,
) {
	// Print a blank line before the final report.
	logger.Println("")

	// Print the report separator.
	logger.Println("========================================")

	// Print the report title.
	logger.Println("SCRAPING COMPLETE")

	// Print the report separator.
	logger.Println("========================================")

	// Print collection pages visited.
	logger.Printf(
		"Collection pages visited: %d",
		scrapingStatistics.TotalCollectionPagesVisited,
	)

	// Print products discovered.
	logger.Printf(
		"Products discovered:      %d",
		scrapingStatistics.TotalProductsDiscovered,
	)

	// Print products visited.
	logger.Printf(
		"Products visited:         %d",
		scrapingStatistics.TotalProductsVisited,
	)

	// Print products with PDFs.
	logger.Printf(
		"Products with PDFs:       %d",
		scrapingStatistics.ProductsWithPDFs,
	)

	// Print products without PDFs.
	logger.Printf(
		"Products without PDFs:    %d",
		scrapingStatistics.ProductsWithoutPDFs,
	)

	// Print product page errors.
	logger.Printf(
		"Product page errors:      %d",
		scrapingStatistics.ProductPageErrors,
	)

	// Print total PDF links.
	logger.Printf(
		"PDF links found:          %d",
		scrapingStatistics.TotalPDFLinksFound,
	)

	// Print unique PDF URLs.
	logger.Printf(
		"Unique PDF URLs found:    %d",
		scrapingStatistics.UniquePDFURLsFound,
	)

	// Print downloaded PDFs.
	logger.Printf(
		"PDFs downloaded:          %d",
		scrapingStatistics.PDFsDownloaded,
	)

	// Print skipped PDFs.
	logger.Printf(
		"PDFs skipped:             %d",
		scrapingStatistics.PDFsSkipped,
	)

	// Print duplicate PDF contents.
	logger.Printf(
		"Duplicate PDF files:      %d",
		scrapingStatistics.DuplicatePDFFiles,
	)

	// Print PDF download errors.
	logger.Printf(
		"PDF download errors:      %d",
		scrapingStatistics.PDFDownloadErrors,
	)

	// Print the number of unique files currently stored.
	logger.Printf(
		"Unique PDFs on disk:      %d",
		len(pdfDownloadState.PDFFileHashes),
	)

	// Print the output directory.
	logger.Printf(
		"PDF directory:            %s",
		pdfDirectoryName,
	)

	// Print the final separator.
	logger.Println("========================================")
}

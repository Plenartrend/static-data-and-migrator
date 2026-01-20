package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/agnivade/levenshtein"
	"google.golang.org/genai"
)

var geminiClient *genai.Client = nil // lazy initialized

func getGeminiClient() (*genai.Client, error) {
	var err error = nil
	if geminiClient == nil {
		geminiClient, err = genai.NewClient(context.Background(), &genai.ClientConfig{
			APIKey:  os.Getenv("GEMINI_API_KEY"),
			Backend: genai.BackendGeminiAPI,
		})
	}
	return geminiClient, err
}

const geminiModel string = "gemini-2.5-flash"

// createFlexibleWhitespacePattern converts a search string into a regex pattern
// where each space can match various combinations of spaces and newlines
func createFlexibleWhitespacePattern(searchString string) *regexp.Regexp {
	// Escape special regex characters in the search string
	escaped := regexp.QuoteMeta(searchString)
	// Replace spaces with flexible whitespace pattern that matches one or more space/newline characters
	// This matches: " ", "\n", " \n", "\n ", " \n ", etc.
	pattern := strings.ReplaceAll(escaped, ` `, `[ \n]+`)
	return regexp.MustCompile(pattern)
}

func findExactMatches(pattern *regexp.Regexp, haystack string, logger *Logger) []int {
	logger.Debug(fmt.Sprintf("findExactMatches called: pattern: %s", pattern.String()))
	var positions []int
	matches := pattern.FindAllStringIndex(haystack, -1)
	for _, match := range matches {
		positions = append(positions, match[0])
	}
	return positions
}

func findBestMatch(needle string, haystack string, maxDistanceRatio float64, logger *Logger) (startIdx int, found bool) {
	startTime := time.Now()
	needleLen := len(needle)
	haystackLen := len(haystack)

	logger.Debug(fmt.Sprintf("findBestMatch called: needleLen=%d, haystackLen=%d, maxDistanceRatio=%.2f", needleLen, haystackLen, maxDistanceRatio))

	if needleLen == 0 || needleLen > haystackLen {
		logger.Warn(fmt.Sprintf("findBestMatch: invalid input (needleLen=%d, haystackLen=%d). Needle:\n%s", needleLen, haystackLen, needle))
		return -1, false
	}

	maxDistance := int(float64(needleLen) * maxDistanceRatio)
	words := strings.Fields(needle)

	// Try progressively shorter versions by removing words from the end
	// Stop as soon as we find at least one match
	var matchPositions []int
	numWordsUsed := 0
	wordsRemovedFromFront := 0
	removedPrefixLen := 0

	// Phase 1: Remove words from the end
	for numWords := len(words); numWords >= 3; numWords-- {
		searchString := strings.Join(words[:numWords], " ")
		pattern := createFlexibleWhitespacePattern(searchString)

		// Find all occurrences of this search string with flexible whitespace
		matchPositions = findExactMatches(pattern, haystack, logger)

		if len(matchPositions) > 0 {
			// Found matches! Stop removing words and use these
			numWordsUsed = numWords
			logger.Debug(fmt.Sprintf("findBestMatch: found %d matches with %d words (removed from end)", len(matchPositions), numWords))
			break
		}
	}

	// Phase 2: If no matches from removing from end, try removing from the front
	if len(matchPositions) == 0 {
		logger.Debug("findBestMatch: no matches by removing from end, trying to remove from front")

		for numWordsToRemove := 1; numWordsToRemove <= len(words)-3; numWordsToRemove++ {
			searchString := strings.Join(words[numWordsToRemove:], " ")
			removedPrefix := strings.Join(words[:numWordsToRemove], " ") + " "
			pattern := createFlexibleWhitespacePattern(searchString)

			// Find all occurrences of this search string with flexible whitespace
			matchPositions = findExactMatches(pattern, haystack, logger)

			if len(matchPositions) > 0 {
				// Found matches! Remember how many words we removed from front
				wordsRemovedFromFront = numWordsToRemove
				removedPrefixLen = len(removedPrefix)
				numWordsUsed = len(words) - numWordsToRemove
				logger.Debug(fmt.Sprintf("findBestMatch: found %d matches with %d words (removed %d from front)", len(matchPositions), numWordsUsed, wordsRemovedFromFront))
				break
			}
		}
	}

	// Phase 3: Last resort - try 3 words from the middle
	if len(matchPositions) == 0 && len(words) >= 5 {
		logger.Debug("findBestMatch: no matches by removing from end or front, trying 3 middle words")

		// Calculate middle position
		middleStart := (len(words) - 3) / 2
		middleEnd := middleStart + 3
		searchString := strings.Join(words[middleStart:middleEnd], " ")
		pattern := createFlexibleWhitespacePattern(searchString)

		// Calculate removed prefix (words before middle)
		var removedPrefix string
		if middleStart > 0 {
			removedPrefix = strings.Join(words[:middleStart], " ") + " "
		}

		// Find all occurrences of this search string with flexible whitespace
		matchPositions = findExactMatches(pattern, haystack, logger)

		if len(matchPositions) > 0 {
			// Found matches with middle words
			wordsRemovedFromFront = middleStart
			removedPrefixLen = len(removedPrefix)
			numWordsUsed = 3
			logger.Debug(fmt.Sprintf("findBestMatch: found %d matches with 3 middle words (offset by %d words from front)", len(matchPositions), wordsRemovedFromFront))
		}
	}

	if len(matchPositions) == 0 {
		duration := time.Since(startTime)
		logger.Warn(fmt.Sprintf("findBestMatch: no matches found even after trying end, front, and middle in %v. Needle:\n%s", duration, needle))
		return -1, false
	}

	// For each match, calculate Levenshtein distance with original needle and pick the best
	bestDistance := math.MaxInt
	bestIdx := -1

	for _, matchPos := range matchPositions {
		// If we removed words from the front, adjust the position back
		adjustedPos := matchPos
		if wordsRemovedFromFront > 0 {
			// Go back by the length of the removed prefix
			adjustedPos = matchPos - removedPrefixLen
			if adjustedPos < 0 {
				continue // Can't go back that far
			}
		}

		// Extract window of original needle length starting at adjusted position
		if adjustedPos+needleLen > haystackLen {
			continue // Not enough text left
		}

		window := haystack[adjustedPos : adjustedPos+needleLen]
		logger.Debug(fmt.Sprintf("findBestMatch: needle: %s\nwindow: %s", needle, window))
		distance := levenshtein.ComputeDistance(needle, window)

		if distance < bestDistance {
			bestDistance = distance
			bestIdx = adjustedPos
		}

		// Early exit on exact match
		if distance == 0 {
			duration := time.Since(startTime)
			logger.Debug(fmt.Sprintf("findBestMatch: exact match found at index %d in %v", bestIdx, duration))
			return bestIdx, true
		}
	}

	// Check if best match is within acceptable distance
	if bestIdx >= 0 && bestDistance <= maxDistance {
		duration := time.Since(startTime)
		if wordsRemovedFromFront > 0 {
			if numWordsUsed == 3 {
				logger.Debug(fmt.Sprintf("findBestMatch: fuzzy match found at index %d with distance %d (using 3 middle words, offset %d from front) in %v", bestIdx, bestDistance, wordsRemovedFromFront, duration))
			} else {
				logger.Debug(fmt.Sprintf("findBestMatch: fuzzy match found at index %d with distance %d (using %d words, removed %d from front) in %v", bestIdx, bestDistance, numWordsUsed, wordsRemovedFromFront, duration))
			}
		} else {
			logger.Debug(fmt.Sprintf("findBestMatch: fuzzy match found at index %d with distance %d (using %d words) in %v", bestIdx, bestDistance, numWordsUsed, duration))
		}
		return bestIdx, true
	}

	duration := time.Since(startTime)
	logger.Warn(fmt.Sprintf("findBestMatch: no acceptable match found (bestDistance=%d > maxDistance=%d) in %v. Needle:\n%s\n", bestDistance, maxDistance, duration, needle))
	return -1, false
}

func getSpeechByStartAndEnd(firstSentences string, lastSentences string, protocol *Protocol, logger *Logger) (string, error) {
	logger.Debug(fmt.Sprintf("getSpeechByStartAndEnd called with firstSentences (len=%d), lastSentences (len=%d)", len(firstSentences), len(lastSentences)))
	if protocol == nil {
		return "", fmt.Errorf("protocol cannot be nil")
	}

	text := protocol.Text

	// Try exact match first
	startIdx := strings.Index(text, firstSentences)
	if startIdx == -1 {
		// Fall back to fuzzy matching with 30% tolerance
		logger.Debug("Exact match failed for start, trying fuzzy match")
		var found bool
		startIdx, found = findBestMatch(firstSentences, text, 0.25, logger)
		if !found {
			// Log detailed information for debugging - this is expected to happen sometimes
			logger.Warn(fmt.Sprintf("No match found for start (skipping speech). Start text:\n%s\nEnd text:\n%s", firstSentences, lastSentences))
			return "", fmt.Errorf("could not find start of speech - skipping")
		}
		logger.Info(fmt.Sprintf("Found fuzzy match for start at index %d", startIdx))
	}

	// Search for end only after the start
	endSearchText := text[startIdx:]
	endIdx := strings.LastIndex(endSearchText, lastSentences)

	if endIdx == -1 {
		// Fall back to fuzzy matching for end
		logger.Debug("Exact match failed for end, trying fuzzy match")
		var found bool
		endIdx, found = findBestMatch(lastSentences, endSearchText, 0.25, logger)
		if !found {
			// Log detailed information for debugging - this is expected to happen sometimes
			protocolPreview := endSearchText
			if len(protocolPreview) > 1000 {
				protocolPreview = protocolPreview[:1000] + "..."
			}
			logger.Warn(fmt.Sprintf("No match found for end (skipping speech). Start text:\n%s\nEnd text:\n%s", firstSentences, lastSentences))
			return "", fmt.Errorf("could not find end of speech - skipping")
		}
		logger.Info(fmt.Sprintf("Found fuzzy match for end at relative index %d", endIdx))
	}

	endIdx = startIdx + endIdx + len(lastSentences)

	if endIdx <= startIdx {
		logger.Warn(fmt.Sprintf("Invalid speech boundaries: start=%d, end=%d. Start text:\n%s\nEnd text:\n%s", startIdx, endIdx, firstSentences, lastSentences))
		return "", fmt.Errorf("invalid speech boundaries: start=%d, end=%d", startIdx, endIdx)
	}

	logger.Debug(fmt.Sprintf("Extracted speech from index %d to %d (length=%d)", startIdx, endIdx, endIdx-startIdx))
	return text[startIdx:endIdx], nil
}

func testAiClient() {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  os.Getenv("GEMINI_API_KEY"),
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		fmt.Println("Failed to create Gemini client:", err)
		return
	}

	parts := []*genai.Part{
		{Text: "hello"},
	}

	resp, err := client.Models.GenerateContent(context.Background(), geminiModel, []*genai.Content{
		{Parts: parts},
	}, nil)
	if err != nil {
		fmt.Println("Error generating content:", err)
		return
	}

	fmt.Println(resp.Text())
}

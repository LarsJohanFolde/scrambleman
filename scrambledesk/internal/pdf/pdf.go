package pdf

import (
	"fmt"
	"path"
	"scrambledesk-client/config"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func DecryptPDF(inputPath, password string) error {
	fmt.Println(inputPath)
	conf := model.NewAESConfiguration(password, "", 256)

	// Perform decryption.
	outputPath := path.Join(config.AppDataDir, "active.pdf")
	err := api.DecryptFile(inputPath, outputPath, conf)
	if err != nil {
		return fmt.Errorf("decrypt failed: %w", err)
	}
	return nil
}

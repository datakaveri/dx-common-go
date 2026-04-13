package s3

// Config holds settings for connecting to S3 or MinIO.
type Config struct {
	// Provider is "s3" for AWS or "minio" for a local MinIO instance.
	Provider        string `mapstructure:"provider"`
	Endpoint        string `mapstructure:"endpoint"`
	Region          string `mapstructure:"region"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
	Bucket          string `mapstructure:"bucket"`
	UseSSL          bool   `mapstructure:"use_ssl"`
	ForcePathStyle  bool   `mapstructure:"force_path_style"`
}

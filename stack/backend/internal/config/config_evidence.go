package config

// defaultEvidenceQueueName is the base queue every generic evidence-corpus
// connector publishes connector.EvidenceJob bodies to, drained by the generic
// evidence worker (cmd/evidenceworker). It is kept separate from the wiki crawl,
// fact-check, and scrutins queues so the evidence worker never consumes a
// wiki-shaped chunk, a curated claim, or a scrutin.
const defaultEvidenceQueueName = "evidence.chunks"

// LoadEvidenceQueue reads the generic evidence-corpus broker configuration. It
// shares the broker URL, priority, prefetch, and version machinery with LoadQueue
// but binds to its own base queue name (RABBITMQ_EVIDENCE_QUEUE, default
// evidence.chunks), so a non-wiki evidence source's jobs reach only the generic
// evidence worker.
func LoadEvidenceQueue() (Queue, error) {
	return loadQueue("RABBITMQ_EVIDENCE_QUEUE", defaultEvidenceQueueName)
}

// LoadEvidenceWorker reads the generic evidence-worker configuration
// (EVIDENCE_WORKER_*). It reuses the EmbedWorker shape and shared defaults; the
// evidence worker embeds one chunk per job, so it never reads BatchSize/BatchWait,
// which keep their defaults. Only the env prefix differs from LoadCrawlWorker.
func LoadEvidenceWorker() (EmbedWorker, error) {
	return loadWorkerCommon("EVIDENCE_WORKER", defaultWorker())
}

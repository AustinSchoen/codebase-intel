package chunker

type Chunk struct {
	ID            string
	Filepath      string
	Module        string
	QualifiedName string
	Kind          string // function, class, struct, method, etc.
	Language      string
	Content       string
	ContextPrefix string
	LineStart     int
	LineEnd       int
	ParentClass   string
	Dependencies  []string
}

type Chunker struct {
	maxLines     int
	overlapLines int
}

func New(maxLines, overlapLines int) *Chunker {
	return &Chunker{maxLines: maxLines, overlapLines: overlapLines}
}

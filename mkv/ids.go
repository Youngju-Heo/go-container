package mkv

// Matroska/EBML element IDs (stored with marker bits, as they appear on disk).
const (
	idEBML              uint32 = 0x1A45DFA3
	idEBMLVersion       uint32 = 0x4286
	idEBMLReadVersion   uint32 = 0x42F7
	idEBMLMaxIDLength   uint32 = 0x42F2
	idEBMLMaxSizeLength uint32 = 0x42F3
	idDocType           uint32 = 0x4282
	idDocTypeVersion    uint32 = 0x4287
	idDocTypeReadVer    uint32 = 0x4285

	idSegment  uint32 = 0x18538067
	idSeekHead uint32 = 0x114D9B74
	idSeek     uint32 = 0x4DBB
	idSeekID   uint32 = 0x53AB
	idSeekPos  uint32 = 0x53AC
	idVoid     uint32 = 0xEC
	idCRC32    uint32 = 0xBF

	idInfo           uint32 = 0x1549A966
	idTimestampScale uint32 = 0x2AD7B1
	idDuration       uint32 = 0x4489
	idDateUTC        uint32 = 0x4461
	idTitle          uint32 = 0x7BA9
	idMuxingApp      uint32 = 0x4D80
	idWritingApp     uint32 = 0x5741

	idTracks          uint32 = 0x1654AE6B
	idTrackEntry      uint32 = 0xAE
	idTrackNumber     uint32 = 0xD7
	idTrackUID        uint32 = 0x73C5
	idTrackType       uint32 = 0x83
	idFlagLacing      uint32 = 0x9C
	idDefaultDuration uint32 = 0x23E383
	idCodecID         uint32 = 0x86
	idCodecPrivate    uint32 = 0x63A2
	idVideo           uint32 = 0xE0
	idPixelWidth      uint32 = 0xB0
	idPixelHeight     uint32 = 0xBA
	idAudio           uint32 = 0xE1
	idSamplingFreq    uint32 = 0xB5
	idOutSamplingFreq uint32 = 0x78B5
	idChannels        uint32 = 0x9F
	idBitDepth        uint32 = 0x6264

	idCluster     uint32 = 0x1F43B675
	idTimestamp   uint32 = 0xE7
	idSimpleBlock uint32 = 0xA3
	idBlockGroup  uint32 = 0xA0
	idBlock       uint32 = 0xA1
	idBlockDur    uint32 = 0x9B
	idRefBlock    uint32 = 0xFB

	idCues          uint32 = 0x1C53BB6B
	idCuePoint      uint32 = 0xBB
	idCueTime       uint32 = 0xB3
	idCueTrackPos   uint32 = 0xB7
	idCueTrack      uint32 = 0xF7
	idCueClusterPos uint32 = 0xF1

	idTags      uint32 = 0x1254C367
	idTag       uint32 = 0x7373
	idTargets   uint32 = 0x63C0
	idSimpleTag uint32 = 0x67C8
	idTagName   uint32 = 0x45A3
	idTagString uint32 = 0x4487
)

// Matroska TrackType values.
const (
	trackTypeVideo    = 1
	trackTypeAudio    = 2
	trackTypeSubtitle = 17
)

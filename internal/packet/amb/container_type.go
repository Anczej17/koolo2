package amb

// ContainerType represents the type of container for item operations
type ContainerType uint32

const (
	ContainerInventory   ContainerType = 0
	ContainerTrade       ContainerType = 2
	ContainerCube        ContainerType = 3
	ContainerStash       ContainerType = 4
	ContainerSharedStash ContainerType = 5 // Shared stash tabs
	ContainerBelt        ContainerType = 5 // Belt also uses 5 for UseItem packet
)

import {
	chakra,
	HStack,
	IconButton,
	Input,
	InputGroup,
	InputLeftElement,
	InputRightElement,
	type InputGroupProps,
	type InputProps,
	Spinner,
	Text,
	Tooltip,
} from "@chakra-ui/react";
import { MagnifyingGlassIcon, XMarkIcon } from "@heroicons/react/24/outline";
import type { FC, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { SearchMatchOptions } from "utils/searchMatch";

const SearchIcon = chakra(MagnifyingGlassIcon, {
	baseStyle: { w: 4, h: 4 },
});
const ClearIcon = chakra(XMarkIcon, { baseStyle: { w: 3.5, h: 3.5 } });

type SearchInputProps = InputProps & {
	containerProps?: InputGroupProps;
	isLoading?: boolean;
	matchOptions: SearchMatchOptions;
	onClear?: () => void;
	onMatchOptionsChange: (options: SearchMatchOptions) => void;
	rightElement?: ReactNode;
};

export const SearchInput: FC<SearchInputProps> = ({
	containerProps,
	isLoading = false,
	matchOptions,
	onClear,
	onMatchOptionsChange,
	rightElement,
	size = "sm",
	value,
	...inputProps
}) => {
	const { t, i18n } = useTranslation();
	const isRTL = i18n.dir(i18n.resolvedLanguage || i18n.language) === "rtl";
	const toggle = (key: keyof SearchMatchOptions) =>
		onMatchOptionsChange({ ...matchOptions, [key]: !matchOptions[key] });

	return (
		<InputGroup
			size={size}
			{...containerProps}
			dir={isRTL ? "rtl" : "ltr"}
		>
			<InputLeftElement
				pointerEvents="none"
				h="full"
				left={isRTL ? "auto" : 0}
				right={isRTL ? 0 : "auto"}
			>
				<SearchIcon color="panel.textMuted" />
			</InputLeftElement>
			<Input
				value={value}
				ps={isRTL ? "116px" : "10"}
				pe={isRTL ? "10" : "116px"}
				{...inputProps}
			/>
			<InputRightElement
				w="auto"
				pe={1}
				h="full"
				left={isRTL ? 0 : "auto"}
				right={isRTL ? "auto" : 0}
			>
				<HStack spacing={0.5}>
					{isLoading && <Spinner size="xs" color="panel.textMuted" />}
					<Tooltip label={t("search.matchCase")} hasArrow>
						<IconButton
							aria-label={t("search.matchCase")}
							aria-pressed={matchOptions.matchCase}
							icon={<Text fontSize="xs">Aa</Text>}
							onClick={() => toggle("matchCase")}
							type="button"
							size="xs"
							variant="ghost"
							color={matchOptions.matchCase ? "primary.400" : "panel.textMuted"}
							bg={matchOptions.matchCase ? "panel.hover" : "transparent"}
						/>
					</Tooltip>
					<Tooltip label={t("search.matchWholeWord")} hasArrow>
						<IconButton
							aria-label={t("search.matchWholeWord")}
							aria-pressed={matchOptions.matchWholeWord}
							icon={
								<Text fontSize="xs" textDecoration="underline">
									ab
								</Text>
							}
							onClick={() => toggle("matchWholeWord")}
							type="button"
							size="xs"
							variant="ghost"
							color={
								matchOptions.matchWholeWord ? "primary.400" : "panel.textMuted"
							}
							bg={matchOptions.matchWholeWord ? "panel.hover" : "transparent"}
						/>
					</Tooltip>
					{rightElement}
					{onClear && String(value ?? "").length > 0 && (
						<IconButton
							aria-label={t("clear")}
							icon={<ClearIcon />}
							onClick={onClear}
							type="button"
							size="xs"
							variant="ghost"
						/>
					)}
				</HStack>
			</InputRightElement>
		</InputGroup>
	);
};

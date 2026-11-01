import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { PanelSelect, type PanelSelectOption } from "./PanelSelect";

export type SearchableTagSelectOption =
	| string
	| {
			disabled?: boolean;
			label?: string;
			title?: string;
			value: string;
	  };

type SearchableTagSelectProps = {
	emptyText?: string;
	isDisabled?: boolean;
	mode?: "single" | "multiple";
	onChange: (value: string | string[]) => void;
	options: SearchableTagSelectOption[];
	placeholder: string;
	rightElement?: ReactNode;
	searchPlaceholder?: string;
	size?: "sm" | "md";
	value: string | string[];
	width?: string;
};

const normalizeOption = (
	option: SearchableTagSelectOption,
): PanelSelectOption => {
	if (typeof option === "string") {
		return {
			label: option,
			title: option,
			value: option,
		};
	}
	return {
		disabled: option.disabled,
		label: option.label || option.title || option.value,
		title: option.title || option.label || option.value,
		value: option.value,
	};
};

export const SearchableTagSelect = ({
	emptyText,
	isDisabled = false,
	mode = "single",
	onChange,
	options,
	placeholder,
	rightElement,
	searchPlaceholder,
	size = "sm",
	value,
	width,
}: SearchableTagSelectProps) => {
	const { t } = useTranslation();
	return (
		<PanelSelect
			mode={mode}
			value={value}
			options={options.map(normalizeOption)}
			placeholder={placeholder}
			rightElement={rightElement}
			searchPlaceholder={searchPlaceholder ?? t("search")}
			emptyText={emptyText ?? t("hostsDialog.noAutocompleteOptions")}
			isDisabled={isDisabled}
			size={size}
			width={width}
			onValueChange={onChange}
		/>
	);
};
